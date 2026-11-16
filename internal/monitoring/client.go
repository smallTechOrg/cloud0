// Package monitoring fetches the Metrics-tab families from Cloud Monitoring.
// client.go is the shared helper (frozen contract); each family lives in its
// own file (vm.go, cloudsql.go, cloudrun.go, gcs.go, logging.go).
package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	monitoringapi "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// RoleHint and API are used when classifying Monitoring errors.
const (
	RoleHint = "roles/monitoring.viewer"
	API      = "monitoring.googleapis.com"
)

// Lister is the one Monitoring call every family needs; tests inject a fake.
type Lister interface {
	ListTimeSeries(ctx context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error)
}

type gcpLister struct{ c *monitoringapi.MetricClient }

// NewLister builds a real Lister using Application Default Credentials
// (GOOGLE_APPLICATION_CREDENTIALS locally). Call close when done.
func NewLister(ctx context.Context) (Lister, func() error, error) {
	c, err := monitoringapi.NewMetricClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("monitoring: new client: %w", err)
	}
	return gcpLister{c: c}, c.Close, nil
}

func (g gcpLister) ListTimeSeries(ctx context.Context, req *monitoringpb.ListTimeSeriesRequest) ([]*monitoringpb.TimeSeries, error) {
	it := g.c.ListTimeSeries(ctx, req)
	var out []*monitoringpb.TimeSeries
	for {
		ts, err := it.Next()
		if err == iterator.Done {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, ts)
	}
}

// Query is one ListTimeSeries call aligned to the whole window.
type Query struct {
	Project string
	Filter  string
	Aligner monitoringpb.Aggregation_Aligner
	Reducer monitoringpb.Aggregation_Reducer // REDUCE_NONE when unset
	GroupBy []string
	Window  gcpsource.Window
}

// Request builds the API request: one point per series covering the window.
func (q Query) Request() *monitoringpb.ListTimeSeriesRequest {
	agg := &monitoringpb.Aggregation{
		AlignmentPeriod:    durationpb.New(q.Window.End.Sub(q.Window.Start)),
		PerSeriesAligner:   q.Aligner,
		CrossSeriesReducer: q.Reducer,
		GroupByFields:      q.GroupBy,
	}
	return &monitoringpb.ListTimeSeriesRequest{
		Name:   "projects/" + q.Project,
		Filter: q.Filter,
		Interval: &monitoringpb.TimeInterval{
			StartTime: timestamppb.New(q.Window.Start),
			EndTime:   timestamppb.New(q.Window.End),
		},
		Aggregation: agg,
		View:        monitoringpb.ListTimeSeriesRequest_FULL,
	}
}

// Provenance records the exact filter and aggregation for cell inspection.
func (q Query) Provenance(source string, fetchedAt time.Time) gcpsource.Provenance {
	agg, _ := json.Marshal(map[string]any{
		"project":              q.Project,
		"alignment_period_s":   int64(q.Window.End.Sub(q.Window.Start).Seconds()),
		"per_series_aligner":   q.Aligner.String(),
		"cross_series_reducer": q.Reducer.String(),
		"group_by_fields":      q.GroupBy,
	})
	return gcpsource.Provenance{
		Source: source, Query: q.Filter, Aggregation: string(agg),
		Window: q.Window, FetchedAt: fetchedAt.UTC(),
	}
}

// Run executes the query.
func (q Query) Run(ctx context.Context, l Lister) ([]*monitoringpb.TimeSeries, error) {
	return l.ListTimeSeries(ctx, q.Request())
}

// Value returns the summed value of all points in a series (one point when
// aligned to the window). Handles double, int64 and distribution (mean*count).
func Value(ts *monitoringpb.TimeSeries) float64 {
	var sum float64
	for _, p := range ts.GetPoints() {
		v := p.GetValue()
		switch x := v.GetValue().(type) {
		case *monitoringpb.TypedValue_DoubleValue:
			sum += x.DoubleValue
		case *monitoringpb.TypedValue_Int64Value:
			sum += float64(x.Int64Value)
		case *monitoringpb.TypedValue_DistributionValue:
			d := x.DistributionValue
			sum += d.GetMean() * float64(d.GetCount())
		}
	}
	return sum
}

// Label reads key from metric labels, then resource labels, then metadata
// system/user labels ("" if absent). compute.* and agent.* differ in where
// the instance identity lives, so callers can ask for either.
func Label(ts *monitoringpb.TimeSeries, key string) string {
	if v, ok := ts.GetMetric().GetLabels()[key]; ok {
		return v
	}
	if v, ok := ts.GetResource().GetLabels()[key]; ok {
		return v
	}
	if md := ts.GetMetadata(); md != nil {
		if v, ok := md.GetUserLabels()[key]; ok {
			return v
		}
		if f, ok := md.GetSystemLabels().GetFields()[key]; ok {
			return f.GetStringValue()
		}
	}
	return ""
}
