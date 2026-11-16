package budgets

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

// now is a Tuesday of ISO week 2026-W44, so the last complete week is W43.
var now = time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), testenv.TempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func seedWeek(t *testing.T, s *store.Store, week string, rows []store.SnapshotRow) {
	t.Helper()
	ctx := context.Background()
	w := isoweek.MustParse(week)
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		run, err := store.CreateRun(ctx, tx, store.NewRun{Trigger: store.TriggerRefreshWeek, From: w, To: w, StartedAt: now})
		if err != nil {
			return err
		}
		src, err := store.AddSource(ctx, tx, store.NewRunSource{RunID: run.ID, Source: store.SourceBigQueryBilling, Status: store.SourceOK, StartedAt: now, FinishedAt: now.Add(time.Second)})
		if err != nil {
			return err
		}
		if _, err := store.InsertSnapshot(ctx, tx, store.NewSnapshot{Week: w, FetchedAt: now, StatusAtFetch: isoweek.StatusFinal, RunID: run.ID, RunSourceID: src.ID, Rows: rows}); err != nil {
			return err
		}
		return store.FinishRun(ctx, tx, run.ID, store.RunOK, now.Add(2*time.Second), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// weekRows: compute 700, cloud run 70, networking 10 (below the minimum), other 20 => 800 net.
func weekRows() []store.SnapshotRow {
	return []store.SnapshotRow{
		{ProjectID: "proj-a", ServiceID: "6F81-5844-456A", Service: "Compute Engine", SKUID: "s1", SKU: "core", Gross: 800, Credits: -100},
		{ProjectID: "proj-a", ServiceID: "152E-C115-5142", Service: "Cloud Run", SKUID: "s2", SKU: "cpu", Gross: 70},
		{ProjectID: "proj-b", ServiceID: "E505-1604-58F8", Service: "Networking", SKUID: "s3", SKU: "egress", Gross: 10},
		{ProjectID: "proj-b", ServiceID: "ZZZZ-0000-1111", Service: "Cloud Logging", SKUID: "s4", SKU: "ing", Gross: 20},
	}
}

func byScope(sg []alerting.Suggestion, svc string) *alerting.Suggestion {
	for i := range sg {
		if sg[i].ServiceName == svc {
			return &sg[i]
		}
	}
	return nil
}

func TestSuggestions_FourWeeks(t *testing.T) {
	s := openStore(t)
	for _, w := range []string{"2026-W40", "2026-W41", "2026-W42", "2026-W43"} {
		seedWeek(t, s, w, weekRows())
	}
	sg, err := NewSuggester(s, store.KeyMonthlyTarget, func() time.Time { return now }).Suggestions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acc := sg[0]
	// 1.2 × 800 × 30/7 = 4114.28 -> 4100
	if acc.Scope != "account" || acc.Amount != 4100 || acc.Basis.Method != "snapshots_avg" || acc.Basis.WeeksUsed != 4 || acc.Basis.AvgWeekly != 800 || acc.Basis.Multiplier != 1.2 {
		t.Errorf("account = %+v", acc)
	}
	if strings.Contains(acc.Basis.Explanation, "only") || acc.Draft.DisplayName != "cloud0-account-monthly" || acc.Draft.Amount != 4100 || len(acc.Draft.Thresholds) != 4 || acc.Draft.Severity != "ERROR" || len(acc.Draft.ServiceIDs) != 0 {
		t.Errorf("account basis/draft = %+v", acc)
	}
	ce := byScope(sg, "Compute Engine")
	// 1.2 × 700 × 30/7 = 3600
	if ce == nil || ce.Amount != 3600 || ce.ServiceID != "services/6F81-5844-456A" || ce.Draft.ServiceIDs[0] != ce.ServiceID || ce.Draft.DisplayName != "cloud0-compute-engine-monthly" {
		t.Errorf("compute = %+v", ce)
	}
	if cr := byScope(sg, "Cloud Run"); cr == nil || cr.Amount != 400 { // 1.2×70×30/7=360 -> 400
		t.Errorf("cloud run = %+v", cr)
	}
	if byScope(sg, "Networking") != nil { // 10/week = 42.9/month < 50
		t.Error("Networking under the minimum must be skipped")
	}
	if byScope(sg, "Cloud SQL") != nil || byScope(sg, "Cloud Logging") != nil {
		t.Error("services without data / not in the list must be omitted")
	}
}

func TestSuggestions_FewerThanFourWeeksFlagged(t *testing.T) {
	s := openStore(t)
	seedWeek(t, s, "2026-W42", weekRows())
	seedWeek(t, s, "2026-W43", weekRows())
	sg, err := NewSuggester(s, store.KeyMonthlyTarget, func() time.Time { return now }).Suggestions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sg[0].Basis.WeeksUsed != 2 || sg[0].Amount != 4100 || !strings.Contains(sg[0].Basis.Explanation, "only 2 of 4 weeks") {
		t.Errorf("account = %+v", sg[0])
	}
}

func TestSuggestions_NoDataAndCurrentWeekIgnored(t *testing.T) {
	s := openStore(t)
	seedWeek(t, s, "2026-W44", weekRows()) // current, incomplete week: never used
	sg, err := NewSuggester(s, store.KeyMonthlyTarget, func() time.Time { return now }).Suggestions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sg) != 1 || sg[0].Amount != 0 || sg[0].Basis.Method != "none" || !strings.Contains(sg[0].Basis.Explanation, "refresh") {
		t.Errorf("sg = %+v", sg)
	}
}

func TestSuggestions_MonthlyTargetSettingPreferred(t *testing.T) {
	s := openStore(t)
	seedWeek(t, s, "2026-W43", weekRows())
	if err := store.PutSettings(context.Background(), s.DB, map[string]string{store.KeyMonthlyTarget: "7500"}, now); err != nil {
		t.Fatal(err)
	}
	sg, err := NewSuggester(s, store.KeyMonthlyTarget, func() time.Time { return now }).Suggestions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sg[0].Amount != 7500 || sg[0].Basis.Method != "monthly_total_setting" || sg[0].Basis.Multiplier != 1 || sg[0].Draft.Amount != 7500 {
		t.Errorf("account = %+v", sg[0])
	}
	// service suggestions still come from snapshots
	if byScope(sg, "Compute Engine") == nil {
		t.Error("service suggestion missing")
	}
}

type fakeBudgets struct{ alerting.BudgetClient }

func (fakeBudgets) List(context.Context) ([]alerting.BudgetView, error) {
	return []alerting.BudgetView{
		{Name: "n1", Managed: true, ServiceIDs: []string{"services/6F81-5844-456A"}},
		{Name: "n2", Managed: false}, // unmanaged account budget does not count
	}, nil
}

func TestSuggestions_ExistsFromManagedBudgets(t *testing.T) {
	s := openStore(t)
	seedWeek(t, s, "2026-W43", weekRows())
	sg, err := NewSuggesterWithBudgets(s, store.KeyMonthlyTarget, func() time.Time { return now }, fakeBudgets{}).Suggestions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sg[0].Exists || !byScope(sg, "Compute Engine").Exists || byScope(sg, "Cloud Run").Exists {
		t.Errorf("exists flags wrong: %+v", sg)
	}
}

func TestRoundStep(t *testing.T) {
	for in, want := range map[float64]float64{0: 0, -3: 0, 1: 100, 49: 100, 149: 100, 150: 200, 4114.28: 4100} {
		if got := roundStep(in); got != want {
			t.Errorf("roundStep(%v) = %v, want %v", in, got, want)
		}
	}
}
