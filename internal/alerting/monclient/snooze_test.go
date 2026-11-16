package monclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	monitoring "cloud.google.com/go/monitoring/apiv3/v2"
	"cloud.google.com/go/monitoring/apiv3/v2/monitoringpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/smallTechOrg/cloud0/internal/alerting"
)

func goodSnooze() alerting.SnoozeDraft {
	return alerting.SnoozeDraft{
		Project: "proj-a", DisplayName: "deploy", DurationSeconds: 3600,
		PolicyNames: []string{"projects/proj-a/alertPolicies/1"},
	}
}

func TestValidateSnoozeDraft(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 500, time.UTC)
	s, e, err := ValidateSnoozeDraft(goodSnooze(), now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Equal(now.Truncate(time.Second)) || e.Sub(s) != time.Hour {
		t.Fatalf("window: %v %v", s, e)
	}
	later := now.Truncate(time.Second).Add(2 * time.Hour)
	d := goodSnooze()
	d.StartTime = &later
	d.DurationSeconds = SnoozeMaxSeconds
	s, e, err = ValidateSnoozeDraft(d, now)
	if err != nil || e.Sub(s) != 30*24*time.Hour || !s.Equal(later) {
		t.Fatalf("scheduled: %v %v %v", s, e, err)
	}
}

func TestValidateSnoozeDraftRejects(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	old := now.Add(-48 * time.Hour)
	cases := map[string]func(*alerting.SnoozeDraft){
		"no project":    func(d *alerting.SnoozeDraft) { d.Project = "" },
		"no name":       func(d *alerting.SnoozeDraft) { d.DisplayName = "" },
		"no policies":   func(d *alerting.SnoozeDraft) { d.PolicyNames = nil },
		"bad policy":    func(d *alerting.SnoozeDraft) { d.PolicyNames = []string{"alertPolicies/1"} },
		"other project": func(d *alerting.SnoozeDraft) { d.PolicyNames = []string{"projects/proj-b/alertPolicies/1"} },
		"too short":     func(d *alerting.SnoozeDraft) { d.DurationSeconds = 299 },
		"zero":          func(d *alerting.SnoozeDraft) { d.DurationSeconds = 0 },
		"negative":      func(d *alerting.SnoozeDraft) { d.DurationSeconds = -3600 },
		"too long":      func(d *alerting.SnoozeDraft) { d.DurationSeconds = SnoozeMaxSeconds + 1 },
		"start in past": func(d *alerting.SnoozeDraft) { d.StartTime = &old },
	}
	for name, mut := range cases {
		d := goodSnooze()
		mut(&d)
		if _, _, err := ValidateSnoozeDraft(d, now); !errors.Is(err, alerting.ErrInvalidParams) {
			t.Errorf("%s: want ErrInvalidParams, got %v", name, err)
		}
	}
}

func TestSnoozeStateAndView(t *testing.T) {
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	mk := func(startOff, endOff time.Duration) *monitoringpb.Snooze {
		return &monitoringpb.Snooze{
			Name: "projects/proj-a/snoozes/s1", DisplayName: "x",
			Criteria: &monitoringpb.Snooze_Criteria{Policies: []string{"projects/proj-a/alertPolicies/1"}},
			Interval: &monitoringpb.TimeInterval{StartTime: tsOf(now.Add(startOff)), EndTime: tsOf(now.Add(endOff))},
		}
	}
	for want, sn := range map[string]*monitoringpb.Snooze{
		"active":    mk(-time.Minute, time.Hour),
		"scheduled": mk(time.Minute, time.Hour),
		"expired":   mk(-time.Hour, -time.Minute),
		"expired ":  mk(-time.Hour, 0),
	} {
		if got := snoozeView(sn, now).State; got != strings.TrimSpace(want) {
			t.Errorf("want %s got %s", want, got)
		}
	}
	v := snoozeView(mk(-time.Minute, time.Hour), now)
	if v.Project != "proj-a" || v.Name != "projects/proj-a/snoozes/s1" || len(v.PolicyNames) != 1 || !v.EndTime.After(v.StartTime) {
		t.Fatalf("view: %+v", v)
	}
}

func TestSnoozeWithoutWriterAndBadName(t *testing.T) {
	s := &SnoozeClient{now: time.Now}
	ctx := context.Background()
	if _, err := s.Create(ctx, goodSnooze()); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.End(ctx, "projects/proj-a/snoozes/s1", time.Now()); !errors.Is(err, alerting.ErrNotConfigured) {
		t.Fatalf("end: %v", err)
	}
	if _, err := s.End(ctx, "nope", time.Now()); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("bad name: %v", err)
	}
	bad := goodSnooze()
	bad.DurationSeconds = 1
	if _, err := s.Create(ctx, bad); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("invalid before credentials check: %v", err)
	}
}

// ---- real GCP --------------------------------------------------------------

func TestSnoozeRealListReadOnly(t *testing.T) {
	projects := usProjects(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, err := NewSnoozeClient(ctx, Creds{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, p := range projects {
		list, err := s.List(ctx, p)
		if err != nil {
			t.Fatalf("list project #%d: %v", i, err)
		}
		for _, sn := range list {
			if sn.Name == "" || sn.State == "" || sn.Project != p {
				t.Fatalf("incomplete snooze: %+v", sn)
			}
		}
	}
}

func TestSnoozeRealCreateAndEnd(t *testing.T) {
	project := usProjects(t)[0]
	creds := usWriterCreds(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// Throwaway disabled policy, created directly through the monitoring client.
	pc, err := monitoring.NewAlertPolicyClient(ctx, creds.Writer...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() }) // registered first => runs last
	suffix := time.Now().UTC().Format("150405")
	pol, err := pc.CreateAlertPolicy(ctx, &monitoringpb.CreateAlertPolicyRequest{
		Name: "projects/" + project,
		AlertPolicy: &monitoringpb.AlertPolicy{
			DisplayName: alerting.TestPrefix + "snooze-target-" + suffix,
			Enabled:     boolWrap(false),
			Combiner:    monitoringpb.AlertPolicy_OR,
			Conditions: []*monitoringpb.AlertPolicy_Condition{{
				DisplayName: "cloud0-test-cond",
				Condition: &monitoringpb.AlertPolicy_Condition_ConditionThreshold{ConditionThreshold: &monitoringpb.AlertPolicy_Condition_MetricThreshold{
					Filter:         `metric.type="compute.googleapis.com/instance/cpu/utilization" resource.type="gce_instance"`,
					Comparison:     monitoringpb.ComparisonType_COMPARISON_GT,
					ThresholdValue: 0.99,
					Duration:       durationpb.New(10 * time.Minute),
				}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("create throwaway policy: %v", err)
	}
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
		defer dcancel()
		if err := pc.DeleteAlertPolicy(dctx, &monitoringpb.DeleteAlertPolicyRequest{Name: pol.GetName()}); err != nil {
			t.Errorf("LEAKED RESOURCE: failed to delete test policy %s: %v", pol.GetName(), err)
		}
	})

	sc, err := NewSnoozeClient(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sc.Close() })
	created, err := sc.Create(ctx, alerting.SnoozeDraft{
		Project: project, DisplayName: alerting.TestPrefix + "snooze-" + suffix,
		PolicyNames: []string{pol.GetName()}, DurationSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("create snooze: %v", err)
	}
	// Snoozes cannot be deleted; ending is the only cleanup. Leave it ended and
	// fail loudly if that did not work.
	ended := false
	t.Cleanup(func() {
		if ended {
			return
		}
		ectx, ecancel := context.WithTimeout(context.Background(), time.Minute)
		defer ecancel()
		if _, err := sc.End(ectx, created.Name, time.Now()); err != nil {
			t.Errorf("LEAKED RESOURCE: failed to end test snooze %s: %v", created.Name, err)
		}
	})
	if created.State != "active" || created.Project != project || len(created.PolicyNames) != 1 || created.PolicyNames[0] != pol.GetName() ||
		created.EndTime.Sub(created.StartTime) != time.Hour {
		t.Fatalf("created: %+v", created)
	}
	// Monitoring's List is eventually consistent (a new snooze can take minutes
	// to appear), so verify the created snooze with a strongly consistent Get;
	// List itself is covered by TestSnoozeRealListReadOnly.
	cur, err := sc.writer.GetSnooze(ctx, &monitoringpb.GetSnoozeRequest{Name: created.Name})
	if err != nil || cur.GetDisplayName() != created.DisplayName || len(cur.GetCriteria().GetPolicies()) != 1 {
		t.Fatalf("get created snooze: %v %v", cur, err)
	}

	got, err := sc.End(ctx, created.Name, time.Now())
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	ended = true
	if got.State != "expired" || got.EndTime.After(time.Now().Add(time.Second)) || got.EndTime.Before(got.StartTime) {
		t.Fatalf("after end: %+v", got)
	}
	after, err := sc.writer.GetSnooze(ctx, &monitoringpb.GetSnoozeRequest{Name: created.Name})
	if err != nil {
		t.Fatal(err)
	}
	if st := snoozeView(after, time.Now()).State; st != "expired" {
		t.Fatalf("snooze not ended on the server, state=%s interval=%v", st, after.GetInterval())
	}
	if _, err := sc.End(ctx, created.Name, time.Now()); !errors.Is(err, alerting.ErrInvalidParams) {
		t.Fatalf("second end want ErrInvalidParams, got %v", err)
	}
}

func tsOf(t time.Time) *timestamppb.Timestamp { return timestamppb.New(t) }
func boolWrap(b bool) *wrapperspb.BoolValue   { return wrapperspb.Bool(b) }
