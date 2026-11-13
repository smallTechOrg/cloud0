package errorsrc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	errorreporting "cloud.google.com/go/errorreporting/apiv1beta1"
	"cloud.google.com/go/errorreporting/apiv1beta1/errorreportingpb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// Error Reporting source identity and the fix hints used when it fails.
const (
	SourceErrorReporting = "errors_error_reporting"
	ErrorReportingRole   = "roles/errorreporting.viewer"
	ErrorReportingAPI    = "clouderrorreporting.googleapis.com"

	// GapOutOfRetention marks a window older than Error Reporting keeps stats.
	GapOutOfRetention  = "out of retention"
	HintOutOfRetention = "Error Reporting keeps 30 days of stats"
	// UnknownService labels a group with no service context.
	UnknownService = "(unknown service)"
)

// ErrOutOfRetention is returned when the window starts before the longest
// Error Reporting query period (30 days back from now).
var ErrOutOfRetention = errors.New("errorsrc: window is older than Error Reporting's 30-day retention")

// GroupStat is one error group's occurrences inside a window.
type GroupStat struct {
	Service string
	GroupID string
	Count   int64
}

// GroupStatsLister lists error-group occurrence counts inside a window.
type GroupStatsLister interface {
	ListGroupStats(ctx context.Context, project string, w gcpsource.Window) ([]GroupStat, error)
}

// ERQuery is the fixed-period request the API accepts for a window: the
// API's time range is always [now-period, now], so we request per-bucket
// TimedCounts aligned at the window start and keep only in-window buckets.
type ERQuery struct {
	Period        errorreportingpb.QueryTimeRange_Period
	Bucket        time.Duration
	Alignment     errorreportingpb.TimedCountAlignment
	AlignmentTime time.Time
}

var erPeriods = []struct {
	p errorreportingpb.QueryTimeRange_Period
	d time.Duration
}{
	{errorreportingpb.QueryTimeRange_PERIOD_1_HOUR, time.Hour},
	{errorreportingpb.QueryTimeRange_PERIOD_6_HOURS, 6 * time.Hour},
	{errorreportingpb.QueryTimeRange_PERIOD_1_DAY, 24 * time.Hour},
	{errorreportingpb.QueryTimeRange_PERIOD_1_WEEK, 7 * 24 * time.Hour},
	{errorreportingpb.QueryTimeRange_PERIOD_30_DAYS, 30 * 24 * time.Hour},
}

// PlanErrorReportingQuery picks the smallest fixed period that reaches back
// from now to w.Start. It returns ErrOutOfRetention when none does and an
// error for an empty/inverted window.
func PlanErrorReportingQuery(now time.Time, w gcpsource.Window) (ERQuery, error) {
	if !w.End.After(w.Start) {
		return ERQuery{}, fmt.Errorf("errorsrc: empty window %s..%s", w.Start.Format(time.RFC3339), w.End.Format(time.RFC3339))
	}
	now = now.UTC()
	start := w.Start.UTC()
	bucket := time.Hour
	if w.End.Sub(w.Start)%(24*time.Hour) == 0 {
		bucket = 24 * time.Hour
	}
	for _, c := range erPeriods {
		if !now.Add(-c.d).After(start) {
			return ERQuery{
				Period:        c.p,
				Bucket:        bucket,
				Alignment:     errorreportingpb.TimedCountAlignment_ALIGNMENT_EQUAL_ROUNDED,
				AlignmentTime: start,
			}, nil
		}
	}
	return ERQuery{}, ErrOutOfRetention
}

// Describe renders the request for provenance.
func (q ERQuery) Describe(project string) string {
	return fmt.Sprintf("projects/%s groupStats time_range.period=%s timed_count_duration=%s alignment=%s alignment_time=%s",
		project, q.Period, q.Bucket, q.Alignment, q.AlignmentTime.UTC().Format(time.RFC3339))
}

// Params renders the request parameters as JSON for provenance.
func (q ERQuery) Params(w gcpsource.Window) string {
	b, _ := json.Marshal(map[string]string{
		"time_range.period":    q.Period.String(),
		"timed_count_duration": q.Bucket.String(),
		"alignment":            q.Alignment.String(),
		"alignment_time":       q.AlignmentTime.UTC().Format(time.RFC3339),
		"window_rule":          "sum timed_counts with start_time in [" + w.Start.UTC().Format(time.RFC3339) + ", " + w.End.UTC().Format(time.RFC3339) + ")",
		"group_by":             "representative.service_context.service, else affected_services[0].service",
	})
	return string(b)
}

// countInWindow sums the timed counts whose bucket starts inside [Start, End).
// Buckets are aligned at w.Start, so no bucket straddles the window start;
// clipped buckets at the query-range edge are attributed by their start.
func countInWindow(tcs []*errorreportingpb.TimedCount, w gcpsource.Window) int64 {
	var n int64
	for _, tc := range tcs {
		if tc == nil || tc.GetStartTime() == nil {
			continue
		}
		s := tc.GetStartTime().AsTime()
		if !s.Before(w.Start) && s.Before(w.End) {
			n += tc.GetCount()
		}
	}
	return n
}

// serviceOf names the service a group belongs to.
func serviceOf(gs *errorreportingpb.ErrorGroupStats) string {
	if s := gs.GetRepresentative().GetServiceContext().GetService(); s != "" {
		return s
	}
	for _, sc := range gs.GetAffectedServices() {
		if s := sc.GetService(); s != "" {
			return s
		}
	}
	return UnknownService
}

// toGroupStats converts API stats to in-window GroupStats, dropping groups
// with no occurrences inside the window.
func toGroupStats(in []*errorreportingpb.ErrorGroupStats, w gcpsource.Window) []GroupStat {
	var out []GroupStat
	for _, gs := range in {
		n := countInWindow(gs.GetTimedCounts(), w)
		if n <= 0 {
			continue
		}
		out = append(out, GroupStat{Service: serviceOf(gs), GroupID: gs.GetGroup().GetGroupId(), Count: n})
	}
	return out
}

type erLister struct {
	c   *errorreporting.ErrorStatsClient
	now func() time.Time
}

// NewErrorReportingLister builds the real lister on ErrorStatsClient using
// Application Default Credentials. The returned func closes the client.
func NewErrorReportingLister(ctx context.Context) (GroupStatsLister, func() error, error) {
	c, err := errorreporting.NewErrorStatsClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("errorsrc: error reporting client: %w", err)
	}
	return &erLister{c: c, now: time.Now}, c.Close, nil
}

func (l *erLister) ListGroupStats(ctx context.Context, project string, w gcpsource.Window) ([]GroupStat, error) {
	q, err := PlanErrorReportingQuery(l.now(), w)
	if err != nil {
		return nil, err
	}
	req := &errorreportingpb.ListGroupStatsRequest{
		ProjectName:        "projects/" + project,
		TimeRange:          &errorreportingpb.QueryTimeRange{Period: q.Period},
		TimedCountDuration: durationpb.New(q.Bucket),
		Alignment:          q.Alignment,
		AlignmentTime:      timestamppb.New(q.AlignmentTime),
		PageSize:           100,
	}
	var all []*errorreportingpb.ErrorGroupStats
	it := l.c.ListGroupStats(ctx, req)
	for {
		gs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("errorsrc: list group stats %s: %w", project, err)
		}
		all = append(all, gs)
	}
	return toGroupStats(all, w), nil
}

func i64(v int64) *int64 { return &v }

// FetchErrorReporting returns, per project, one ErrorRow per service (distinct
// groups and summed in-window occurrences) plus a totals row (Service "").
// Zero groups is a valid totals row of zeros. A window older than 30 days is a
// gap row per project. The result fails only when every project fails;
// otherwise failed projects become "source failed" gap rows and the message
// names them.
func FetchErrorReporting(ctx context.Context, l GroupStatsLister, projects []string, w gcpsource.Window, now func() time.Time) gcpsource.Result {
	if now == nil {
		now = time.Now
	}
	res := gcpsource.Result{Source: SourceErrorReporting, Started: now().UTC()}
	q, planErr := PlanErrorReportingQuery(now(), w)
	if planErr != nil && !errors.Is(planErr, ErrOutOfRetention) {
		res.Finish(now().UTC(), planErr, ErrorReportingRole, ErrorReportingAPI)
		return res
	}

	var firstErr error
	var failed []string
	ok := 0
	for _, p := range projects {
		prov := gcpsource.Provenance{Source: SourceErrorReporting, Window: w, FetchedAt: now().UTC()}
		if planErr != nil {
			prov.Query = "projects/" + p + " groupStats not requested: window starts before now-30d (PERIOD_30_DAYS is the longest period)"
			ok++
			res.Provenance = append(res.Provenance, prov)
			res.Errors = append(res.Errors, gcpsource.ErrorRow{
				Source: gcpsource.ErrSrcErrorReporting, Project: p,
				Gap: GapOutOfRetention, Hint: HintOutOfRetention, Provenance: prov,
			})
			continue
		}
		prov.Query = q.Describe(p)
		prov.Aggregation = q.Params(w)

		stats, err := l.ListGroupStats(ctx, p, w)
		prov.FetchedAt = now().UTC()
		res.Provenance = append(res.Provenance, prov)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			failed = append(failed, p)
			_, _, hint := gcpsource.Classify(err, ErrorReportingRole, ErrorReportingAPI)
			res.Errors = append(res.Errors, gcpsource.ErrorRow{
				Source: gcpsource.ErrSrcErrorReporting, Project: p,
				Gap: "source failed", Hint: hint, Provenance: prov,
			})
			continue
		}
		ok++
		res.Errors = append(res.Errors, aggregateGroupStats(p, stats, prov)...)
	}

	if ok == 0 && firstErr != nil {
		res.Finish(now().UTC(), firstErr, ErrorReportingRole, ErrorReportingAPI)
		return res
	}
	if firstErr != nil {
		_, msg, hint := gcpsource.Classify(firstErr, ErrorReportingRole, ErrorReportingAPI)
		res.Message = "skipped projects: " + strings.Join(failed, ", ") + "; " + msg
		res.Hint = hint
	}
	res.Finish(now().UTC(), nil, ErrorReportingRole, ErrorReportingAPI)
	return res
}

// aggregateGroupStats builds per-service rows (sorted by service) and a
// project totals row. Groups with a non-positive count are ignored.
func aggregateGroupStats(project string, stats []GroupStat, prov gcpsource.Provenance) []gcpsource.ErrorRow {
	type agg struct {
		groups map[string]bool
		occ    int64
	}
	by := map[string]*agg{}
	allGroups := map[string]bool{}
	var total int64
	for _, s := range stats {
		if s.Count <= 0 {
			continue
		}
		svc := s.Service
		if svc == "" {
			svc = UnknownService
		}
		a := by[svc]
		if a == nil {
			a = &agg{groups: map[string]bool{}}
			by[svc] = a
		}
		a.groups[s.GroupID] = true
		a.occ += s.Count
		allGroups[s.GroupID] = true
		total += s.Count
	}
	names := make([]string, 0, len(by))
	for k := range by {
		names = append(names, k)
	}
	sort.Strings(names)
	rows := make([]gcpsource.ErrorRow, 0, len(names)+1)
	for _, n := range names {
		a := by[n]
		rows = append(rows, gcpsource.ErrorRow{
			Source: gcpsource.ErrSrcErrorReporting, Project: project, Service: n,
			Groups: i64(int64(len(a.groups))), Occurrences: i64(a.occ), Provenance: prov,
		})
	}
	rows = append(rows, gcpsource.ErrorRow{
		Source: gcpsource.ErrSrcErrorReporting, Project: project, Service: "",
		Groups: i64(int64(len(allGroups))), Occurrences: i64(total), Provenance: prov,
	})
	return rows
}
