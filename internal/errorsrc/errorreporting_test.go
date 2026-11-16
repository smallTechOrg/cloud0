package errorsrc

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/errorreporting/apiv1beta1/errorreportingpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

var erW42 = gcpsource.Window{
	Start: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
	End:   time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC),
}

func erFixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestPlanErrorReportingQuery_PicksSmallestCoveringPeriod(t *testing.T) {
	ws := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	week := gcpsource.Window{Start: ws, End: ws.Add(7 * 24 * time.Hour)}
	cases := []struct {
		name    string
		now     time.Time
		w       gcpsource.Window
		want    errorreportingpb.QueryTimeRange_Period
		bucket  time.Duration
		wantErr error
	}{
		{"30 minutes after start -> 1h", ws.Add(30 * time.Minute), week, errorreportingpb.QueryTimeRange_PERIOD_1_HOUR, 24 * time.Hour, nil},
		{"exactly 1h after start -> 1h", ws.Add(time.Hour), week, errorreportingpb.QueryTimeRange_PERIOD_1_HOUR, 24 * time.Hour, nil},
		{"5h after start -> 6h", ws.Add(5 * time.Hour), week, errorreportingpb.QueryTimeRange_PERIOD_6_HOURS, 24 * time.Hour, nil},
		{"20h after start -> 1 day", ws.Add(20 * time.Hour), week, errorreportingpb.QueryTimeRange_PERIOD_1_DAY, 24 * time.Hour, nil},
		{"exactly 7d after start -> 1 week", ws.Add(7 * 24 * time.Hour), week, errorreportingpb.QueryTimeRange_PERIOD_1_WEEK, 24 * time.Hour, nil},
		{"14d after start (W42 seen on Oct 26) -> 30 days", ws.Add(14 * 24 * time.Hour), week, errorreportingpb.QueryTimeRange_PERIOD_30_DAYS, 24 * time.Hour, nil},
		{"exactly 30d -> 30 days", ws.Add(30 * 24 * time.Hour), week, errorreportingpb.QueryTimeRange_PERIOD_30_DAYS, 24 * time.Hour, nil},
		{"30d + 1s -> out of retention", ws.Add(30*24*time.Hour + time.Second), week, 0, 0, ErrOutOfRetention},
		{"non-day window -> hourly buckets", ws.Add(3 * time.Hour), gcpsource.Window{Start: ws, End: ws.Add(3 * time.Hour)}, errorreportingpb.QueryTimeRange_PERIOD_6_HOURS, time.Hour, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := PlanErrorReportingQuery(tc.now, tc.w)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if q.Period != tc.want || q.Bucket != tc.bucket {
				t.Fatalf("got period %s bucket %s, want %s %s", q.Period, q.Bucket, tc.want, tc.bucket)
			}
			if q.Alignment != errorreportingpb.TimedCountAlignment_ALIGNMENT_EQUAL_ROUNDED || !q.AlignmentTime.Equal(tc.w.Start) {
				t.Fatalf("alignment %s at %s, want EQUAL_ROUNDED at window start", q.Alignment, q.AlignmentTime)
			}
		})
	}
}

func TestPlanErrorReportingQuery_RejectsEmptyWindow(t *testing.T) {
	_, err := PlanErrorReportingQuery(erW42.End, gcpsource.Window{Start: erW42.Start, End: erW42.Start})
	if err == nil || errors.Is(err, ErrOutOfRetention) {
		t.Fatalf("want empty-window error, got %v", err)
	}
}

func tc(start time.Time, d time.Duration, n int64) *errorreportingpb.TimedCount {
	return &errorreportingpb.TimedCount{Count: n, StartTime: timestamppb.New(start), EndTime: timestamppb.New(start.Add(d))}
}

func TestToGroupStats_SumsOnlyInWindowBucketsAndNamesService(t *testing.T) {
	day := 24 * time.Hour
	before := erW42.Start.Add(-day)
	cases := []struct {
		name string
		in   []*errorreportingpb.ErrorGroupStats
		want []GroupStat
	}{
		{
			name: "buckets before, inside and after the window",
			in: []*errorreportingpb.ErrorGroupStats{{
				Group:          &errorreportingpb.ErrorGroup{GroupId: "g1"},
				Count:          100,
				Representative: &errorreportingpb.ErrorEvent{ServiceContext: &errorreportingpb.ServiceContext{Service: "svc-a"}},
				TimedCounts:    []*errorreportingpb.TimedCount{tc(before, day, 50), tc(erW42.Start, day, 3), tc(erW42.End.Add(-day), day, 4), tc(erW42.End, day, 43)},
			}},
			want: []GroupStat{{Service: "svc-a", GroupID: "g1", Count: 7}},
		},
		{
			name: "falls back to affected services, then unknown",
			in: []*errorreportingpb.ErrorGroupStats{
				{Group: &errorreportingpb.ErrorGroup{GroupId: "g2"}, AffectedServices: []*errorreportingpb.ServiceContext{{}, {Service: "svc-b"}}, TimedCounts: []*errorreportingpb.TimedCount{tc(erW42.Start, day, 1)}},
				{Group: &errorreportingpb.ErrorGroup{GroupId: "g3"}, TimedCounts: []*errorreportingpb.TimedCount{tc(erW42.Start.Add(day), day, 2)}},
			},
			want: []GroupStat{{Service: "svc-b", GroupID: "g2", Count: 1}, {Service: UnknownService, GroupID: "g3", Count: 2}},
		},
		{
			name: "group active only outside the window is dropped",
			in: []*errorreportingpb.ErrorGroupStats{{
				Group: &errorreportingpb.ErrorGroup{GroupId: "g4"}, Count: 9,
				TimedCounts: []*errorreportingpb.TimedCount{tc(before, day, 9), {Count: 5}},
			}},
			want: nil,
		},
		{name: "no groups", in: nil, want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toGroupStats(c.in, erW42)
			if len(got) != len(c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("row %d: got %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

type fakeLister struct {
	stats map[string][]GroupStat
	errs  map[string]error
	calls []string
}

func (f *fakeLister) ListGroupStats(_ context.Context, project string, _ gcpsource.Window) ([]GroupStat, error) {
	f.calls = append(f.calls, project)
	if err := f.errs[project]; err != nil {
		return nil, err
	}
	return f.stats[project], nil
}

type wantRow struct {
	project, service string
	groups, occ      int64
	gap              string
}

func checkRows(t *testing.T, got []gcpsource.ErrorRow, want []wantRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d rows %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		r := got[i]
		if r.Source != gcpsource.ErrSrcErrorReporting || r.Project != w.project || r.Service != w.service || r.Gap != w.gap {
			t.Fatalf("row %d = {%s %s %s gap=%q}, want %+v", i, r.Source, r.Project, r.Service, r.Gap, w)
		}
		if w.gap != "" {
			if r.Groups != nil || r.Occurrences != nil || r.Hint == "" {
				t.Fatalf("gap row %d must have nil counts and a hint: %+v", i, r)
			}
			continue
		}
		if r.Groups == nil || r.Occurrences == nil || *r.Groups != w.groups || *r.Occurrences != w.occ {
			t.Fatalf("row %d counts = %v/%v, want %d/%d", i, r.Groups, r.Occurrences, w.groups, w.occ)
		}
	}
}

func TestFetchErrorReporting_BuildsServiceAndTotalsRows(t *testing.T) {
	now := erFixedNow(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	denied := status.Error(codes.PermissionDenied, "caller does not have permission")
	disabled := errors.New("rpc error: SERVICE_DISABLED clouderrorreporting.googleapis.com has not been used in project")
	cases := []struct {
		name       string
		lister     *fakeLister
		projects   []string
		w          gcpsource.Window
		wantStatus string
		wantRows   []wantRow
		wantMsg    string
		wantHint   string
		wantCalls  int
	}{
		{
			name: "two services, shared group counted once in totals",
			lister: &fakeLister{stats: map[string][]GroupStat{"p1": {
				{Service: "web", GroupID: "a", Count: 5},
				{Service: "api", GroupID: "b", Count: 2},
				{Service: "web", GroupID: "c", Count: 1},
				{Service: "", GroupID: "d", Count: 4},
				{Service: "api", GroupID: "z", Count: 0},
			}}},
			projects:   []string{"p1"},
			w:          erW42,
			wantStatus: gcpsource.StatusOK,
			wantRows: []wantRow{
				{"p1", UnknownService, 1, 4, ""},
				{"p1", "api", 1, 2, ""},
				{"p1", "web", 2, 6, ""},
				{"p1", "", 4, 12, ""},
			},
			wantCalls: 1,
		},
		{
			name:       "zero groups is a totals row of zeros, not a gap",
			lister:     &fakeLister{},
			projects:   []string{"p1", "p2"},
			w:          erW42,
			wantStatus: gcpsource.StatusOK,
			wantRows:   []wantRow{{"p1", "", 0, 0, ""}, {"p2", "", 0, 0, ""}},
			wantCalls:  2,
		},
		{
			name:       "window older than 30 days is a gap row without an API call",
			lister:     &fakeLister{},
			projects:   []string{"p1"},
			w:          gcpsource.Window{Start: erW42.Start.AddDate(0, 0, -21), End: erW42.Start.AddDate(0, 0, -14)},
			wantStatus: gcpsource.StatusOK,
			wantRows:   []wantRow{{"p1", "", 0, 0, GapOutOfRetention}},
			wantCalls:  0,
		},
		{
			name:       "every project denied -> permission_denied with role hint",
			lister:     &fakeLister{errs: map[string]error{"p1": denied}},
			projects:   []string{"p1"},
			w:          erW42,
			wantStatus: gcpsource.StatusPermissionDenied,
			wantRows:   []wantRow{{"p1", "", 0, 0, "source failed"}},
			wantHint:   ErrorReportingRole,
			wantCalls:  1,
		},
		{
			name:       "one project API disabled -> ok, partial rows, message names it",
			lister:     &fakeLister{errs: map[string]error{"p2": disabled}, stats: map[string][]GroupStat{"p1": {{Service: "web", GroupID: "a", Count: 1}}}},
			projects:   []string{"p1", "p2"},
			w:          erW42,
			wantStatus: gcpsource.StatusOK,
			wantRows:   []wantRow{{"p1", "web", 1, 1, ""}, {"p1", "", 1, 1, ""}, {"p2", "", 0, 0, "source failed"}},
			wantMsg:    "skipped projects: p2",
			wantHint:   ErrorReportingAPI,
			wantCalls:  2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := FetchErrorReporting(context.Background(), c.lister, c.projects, c.w, now)
			if res.Source != SourceErrorReporting || res.Status != c.wantStatus {
				t.Fatalf("source/status = %s/%s (%s), want %s/%s", res.Source, res.Status, res.Message, SourceErrorReporting, c.wantStatus)
			}
			if !strings.Contains(res.Message, c.wantMsg) || !strings.Contains(res.Hint, c.wantHint) {
				t.Fatalf("message %q hint %q, want containing %q / %q", res.Message, res.Hint, c.wantMsg, c.wantHint)
			}
			if len(c.lister.calls) != c.wantCalls {
				t.Fatalf("lister called %d times, want %d", len(c.lister.calls), c.wantCalls)
			}
			checkRows(t, res.Errors, c.wantRows)
			if len(res.Provenance) != len(c.projects) || res.Finished.IsZero() || res.Started.IsZero() {
				t.Fatalf("provenance/timestamps incomplete: %+v", res)
			}
		})
	}
}

func TestFetchErrorReporting_ProvenanceDescribesRequest(t *testing.T) {
	now := erFixedNow(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))
	res := FetchErrorReporting(context.Background(), &fakeLister{}, []string{"p1"}, erW42, now)
	p := res.Errors[0].Provenance
	for _, want := range []string{"projects/p1", "PERIOD_30_DAYS", "ALIGNMENT_EQUAL_ROUNDED", "2026-10-12T00:00:00Z", "24h0m0s"} {
		if !strings.Contains(p.Query, want) {
			t.Fatalf("query %q missing %q", p.Query, want)
		}
	}
	if !strings.Contains(p.Aggregation, "start_time in [2026-10-12T00:00:00Z, 2026-10-19T00:00:00Z)") || p.Window != erW42 || p.Source != SourceErrorReporting {
		t.Fatalf("provenance = %+v", p)
	}
}

func TestFetchErrorReporting_RealAPIWeek42(t *testing.T) {
	testenv.LoadDotEnv(t)
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") == "" || os.Getenv("CLOUD0_GCP_PROJECTS") == "" {
		t.Skip("GOOGLE_APPLICATION_CREDENTIALS or CLOUD0_GCP_PROJECTS not set — fill .env (see .env.example) to run real Error Reporting tests")
	}
	var projects []string
	for _, p := range strings.Split(os.Getenv("CLOUD0_GCP_PROJECTS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	l, closeFn, err := NewErrorReportingLister(ctx)
	if err != nil {
		t.Fatalf("NewErrorReportingLister: %v", err)
	}
	defer closeFn()

	res := FetchErrorReporting(ctx, l, projects, erW42, time.Now)
	if len(res.Provenance) != len(projects) || !strings.Contains(res.Provenance[0].Query, "PERIOD_30_DAYS") {
		t.Fatalf("W42 seen from late Oct 2026 should request PERIOD_30_DAYS for every project, provenance = %+v", res.Provenance)
	}
	// Environment state, verified 2026-10-26: clouderrorreporting.googleapis.com
	// is disabled on the billed projects, so the real call returns
	// SERVICE_DISABLED. That must surface as api_disabled with the enable hint
	// and one "source failed" row per project — never as zero groups.
	if res.Status == gcpsource.StatusAPIDisabled {
		if res.Hint != "enable "+ErrorReportingAPI+" on the project (gcloud services enable "+ErrorReportingAPI+")" {
			t.Fatalf("api_disabled hint = %q", res.Hint)
		}
		if len(res.Errors) != len(projects) {
			t.Fatalf("want one gap row per project, got %+v", res.Errors)
		}
		for _, r := range res.Errors {
			if r.Gap != "source failed" || r.Groups != nil || r.Hint != res.Hint {
				t.Fatalf("disabled API must give gap rows, got %+v", r)
			}
		}
		t.Logf("REAL: Error Reporting API is disabled (status=%s, hint=%q); enable it on the project to get group counts", res.Status, res.Hint)
		return
	}
	t.Logf("status=%s message=%.300q hint=%q", res.Status, res.Message, res.Hint)
	if res.Status != gcpsource.StatusOK {
		t.Fatalf("status = %s: %s (hint: %s)", res.Status, res.Message, res.Hint)
	}
	totals := 0
	for i, r := range res.Errors {
		if r.Source != gcpsource.ErrSrcErrorReporting {
			t.Fatalf("row source = %s", r.Source)
		}
		if r.Gap != "" {
			t.Logf("project#%d service=%q gap=%q hint=%q", i, r.Service, r.Gap, r.Hint)
			continue
		}
		if r.Groups == nil || r.Occurrences == nil || *r.Groups < 0 || *r.Occurrences < *r.Groups {
			t.Fatalf("bad counts row %+v", r)
		}
		if r.Service == "" {
			totals++
		}
		t.Logf("service=%q groups=%d occurrences=%d", r.Service, *r.Groups, *r.Occurrences)
	}
	if totals == 0 {
		t.Fatalf("no totals row returned")
	}
}
