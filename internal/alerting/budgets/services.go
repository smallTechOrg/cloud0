package budgets

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"cloud.google.com/go/bigquery"
	"cloud.google.com/go/civil"
	"google.golang.org/api/iterator"
)

// Service is one billing-export service.
type Service struct {
	ID   string // services/XXXX-XXXX-XXXX
	Name string // service.description
}

// ServiceLister lists the services seen in the billing export.
type ServiceLister interface {
	ListServices(ctx context.Context) ([]Service, error)
}

// ServiceIDs maps each requested human service name (case-insensitive) to its
// "services/XXXX-XXXX-XXXX" id. Names not found are returned in missing.
func ServiceIDs(ctx context.Context, l ServiceLister, names ...string) (ids map[string]string, missing []string, err error) {
	all, err := l.ListServices(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]string, len(all))
	for _, s := range all {
		byName[strings.ToLower(strings.TrimSpace(s.Name))] = NormalizeServiceID(s.ID)
	}
	ids = make(map[string]string, len(names))
	for _, n := range names {
		if id, ok := byName[strings.ToLower(strings.TrimSpace(n))]; ok && id != "" {
			ids[n] = id
		} else {
			missing = append(missing, n)
		}
	}
	return ids, missing, nil
}

// NormalizeServiceID returns the services/<id> form ("" stays "").
func NormalizeServiceID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	return "services/" + strings.TrimPrefix(id, "services/")
}

var tablePattern = regexp.MustCompile(`^[A-Za-z0-9-]+\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)

// BQServiceLister reads distinct (service.id, service.description) from the
// billing export of the last 14 days, partition-filtered.
type BQServiceLister struct {
	client *bigquery.Client
	table  string
	now    func() time.Time
}

// NewBQServiceLister validates the table id (project.dataset.table).
func NewBQServiceLister(client *bigquery.Client, table string, now func() time.Time) (*BQServiceLister, error) {
	if !tablePattern.MatchString(table) {
		return nil, invalid("billing export table %q is not project.dataset.table", table)
	}
	if now == nil {
		now = time.Now
	}
	return &BQServiceLister{client: client, table: table, now: now}, nil
}

// ServiceQuery is the exact SQL used.
func ServiceQuery(table string) string {
	return "SELECT DISTINCT service.id AS id, service.description AS name\n" +
		"FROM `" + table + "`\n" +
		"WHERE DATE(_PARTITIONTIME) BETWEEN @part_start AND @part_end\n" +
		"  AND usage_start_time >= @usage_start\n" +
		"  AND service.id IS NOT NULL"
}

// ListServices implements ServiceLister.
func (b *BQServiceLister) ListServices(ctx context.Context) ([]Service, error) {
	now := b.now().UTC()
	start := now.AddDate(0, 0, -14)
	q := b.client.Query(ServiceQuery(b.table))
	q.Parameters = []bigquery.QueryParameter{
		{Name: "part_start", Value: civil.DateOf(start.AddDate(0, 0, -1))},
		{Name: "part_end", Value: civil.DateOf(now.AddDate(0, 0, 1))},
		{Name: "usage_start", Value: start},
	}
	it, err := q.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("budgets: service lookup query: %w", err)
	}
	var out []Service
	for {
		var row struct {
			ID   string `bigquery:"id"`
			Name string `bigquery:"name"`
		}
		err := it.Next(&row)
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("budgets: service lookup read: %w", err)
		}
		out = append(out, Service{ID: NormalizeServiceID(row.ID), Name: row.Name})
	}
	return out, nil
}
