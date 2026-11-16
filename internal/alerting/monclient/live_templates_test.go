package monclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/alerting/budgets"
)

// liveForceTest turns a built template policy into a safe test policy: disabled,
// no notification channels, test-prefixed display name.
func liveForceTest(t *testing.T, raw json.RawMessage, suffix string) json.RawMessage {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("built policy is not JSON: %v", err)
	}
	dn, _ := m["displayName"].(string)
	m["displayName"] = alerting.TestPrefix + suffix + "-" + strings.TrimPrefix(dn, "cloud0: ")
	m["enabled"] = false
	delete(m, "notificationChannels")
	delete(m, "name")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// liveNewThreshold picks a different, in-range threshold for the template.
func liveNewThreshold(tpl alerting.Template) *float64 {
	if tpl.Defaults.Threshold == nil {
		return nil
	}
	v := *tpl.Defaults.Threshold + 1
	for _, s := range tpl.ParamSpecs {
		if s.Key == "threshold" && s.Max != nil && v > *s.Max {
			v = *tpl.Defaults.Threshold - 1
		}
	}
	return &v
}

func TestLive_EveryTemplateIsAcceptedByGCP(t *testing.T) {
	project := usProjects(t)[0]
	creds := usWriterCreds(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	pc, err := NewPolicies(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() }) // registered first => runs last
	uc, err := NewUptimeClient(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = uc.Close() })
	checks, err := uc.List(ctx, project)
	if err != nil {
		t.Fatalf("list uptime checks: %v", err)
	}
	if len(checks) == 0 {
		t.Fatal("expected at least one uptime check in the first project for uptime_down")
	}

	b := alerting.NewBuilder()
	type variant struct {
		label  string
		tpl    alerting.Template
		params alerting.TemplateParams
	}
	var variants []variant
	for _, tpl := range b.Templates() {
		if tpl.ID == alerting.TplUptimeDown {
			for _, inst := range []string{"warning", "critical"} {
				variants = append(variants, variant{tpl.ID + "-" + inst, tpl, alerting.TemplateParams{Instance: inst}})
			}
			continue
		}
		variants = append(variants, variant{tpl.ID, tpl, alerting.TemplateParams{}})
	}

	stamp := time.Now().UTC().Format("150405")
	bctx := alerting.BuildContext{Project: project, Now: time.Now(), UptimeChecks: checks}
	for _, v := range variants {
		v := v
		t.Run(v.label, func(t *testing.T) {
			built, err := b.Build(v.tpl, v.params, bctx)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			body := liveForceTest(t, built.Policy, stamp+"-"+v.label)
			want, err := ParsePolicy(body)
			if err != nil {
				t.Fatalf("ParsePolicy: %v", err)
			}
			created, err := pc.Create(ctx, project, body)
			if created.Name != "" {
				name := created.Name
				t.Cleanup(func() {
					cctx, cc := context.WithTimeout(context.Background(), time.Minute)
					defer cc()
					if err := pc.Delete(cctx, name); err != nil && !errors.Is(err, alerting.ErrNotFound) {
						t.Errorf("LOUD: cleanup failed to delete test policy %s: %v", name, err)
					}
				})
			}
			if err != nil {
				t.Fatalf("GCP REJECTED template %s on create: %v\nBODY: %s", v.label, err, body)
			}
			got, err := pc.Get(ctx, created.Name)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			wantSev := want.GetSeverity().String()
			if !got.Managed {
				t.Errorf("managed label missing on round-trip")
			}
			if got.TemplateID != v.tpl.ID {
				t.Errorf("template label = %q, want %q", got.TemplateID, v.tpl.ID)
			}
			if len(got.Conditions) != len(want.GetConditions()) {
				t.Errorf("condition count = %d, want %d", len(got.Conditions), len(want.GetConditions()))
			}
			if !strings.EqualFold(got.Severity, wantSev) {
				t.Errorf("severity = %q, want %q", got.Severity, wantSev)
			}
			if got.Enabled || len(got.Channels) != 0 {
				t.Errorf("test policy must be disabled with no channels: enabled=%v channels=%d", got.Enabled, len(got.Channels))
			}

			// Update once: change the threshold when the template has one,
			// otherwise change the duration, otherwise re-send as is.
			p2 := v.params
			if th := liveNewThreshold(v.tpl); th != nil {
				p2.Threshold = th
			} else if v.tpl.Defaults.DurationSeconds != nil && v.tpl.ID != alerting.TplUptimeDown {
				d := *v.tpl.Defaults.DurationSeconds + 60
				p2.DurationSeconds = &d
			}
			built2, err := b.Build(v.tpl, p2, bctx)
			if err != nil {
				t.Fatalf("Build (updated): %v", err)
			}
			body2 := liveForceTest(t, built2.Policy, stamp+"-"+v.label)
			upd, err := pc.Update(ctx, created.Name, body2)
			if err != nil {
				t.Fatalf("GCP REJECTED template %s on update: %v\nBODY: %s", v.label, err, body2)
			}
			if upd.Name != created.Name || upd.Enabled || len(upd.Conditions) != len(want.GetConditions()) {
				t.Errorf("update result unexpected: name=%s enabled=%v conds=%d", upd.Name, upd.Enabled, len(upd.Conditions))
			}
			if p2.Threshold != nil && len(got.Conditions) > 0 && got.Conditions[0].Threshold != nil && upd.Conditions[0].Threshold != nil &&
				*got.Conditions[0].Threshold == *upd.Conditions[0].Threshold {
				t.Errorf("threshold did not change on update (%v)", *upd.Conditions[0].Threshold)
			}
			if err := pc.Delete(ctx, created.Name); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		})
	}

	// Policy listing is eventually consistent: poll until the deleted test
	// policies disappear, and fail if any cloud0-test- policy remains.
	t.Cleanup(func() {
		lctx, lc := context.WithTimeout(context.Background(), 3*time.Minute)
		defer lc()
		var leftovers []string
		for attempt := 0; attempt < 30; attempt++ {
			leftovers = nil
			left, err := pc.List(lctx, project)
			if err != nil {
				t.Errorf("final list: %v", err)
				return
			}
			for _, v := range left {
				if strings.HasPrefix(v.DisplayName, alerting.TestPrefix) {
					leftovers = append(leftovers, v.Name)
				}
			}
			if len(leftovers) == 0 {
				return
			}
			time.Sleep(5 * time.Second)
		}
		t.Errorf("LEFTOVER cloud0-test- policies remain: %v", leftovers)
	})
}

// TestLive_SweepTestLeftovers removes cloud0-test- resources left by earlier
// runs: policies, uptime checks, budgets deleted; snoozes (which the API cannot
// delete) are ended. Only prefixed resources are touched.
func TestLive_SweepTestLeftovers(t *testing.T) {
	projects := usProjects(t)
	creds := usWriterCreds(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pc, err := NewPolicies(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	uc, err := NewUptimeClient(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer uc.Close()
	sc, err := NewSnoozeClient(ctx, creds)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()

	counts := map[string]int{}
	for _, proj := range projects {
		pols, err := pc.List(ctx, proj)
		if err != nil {
			t.Fatalf("list policies: %v", err)
		}
		for _, v := range pols {
			if strings.HasPrefix(v.DisplayName, alerting.TestPrefix) {
				if err := pc.Delete(ctx, v.Name); err != nil && !errors.Is(err, alerting.ErrNotFound) {
					t.Errorf("delete leftover policy: %v", err)
					continue
				}
				counts["policies deleted"]++
			}
		}
		ups, err := uc.List(ctx, proj)
		if err != nil {
			t.Fatalf("list uptime: %v", err)
		}
		for _, u := range ups {
			if strings.HasPrefix(u.DisplayName, alerting.TestPrefix) {
				if err := uc.Delete(ctx, u.Name); err != nil && !errors.Is(err, alerting.ErrNotFound) {
					t.Errorf("delete leftover uptime check: %v", err)
					continue
				}
				counts["uptime checks deleted"]++
			}
		}
		sns, err := sc.List(ctx, proj)
		if err != nil {
			t.Fatalf("list snoozes: %v", err)
		}
		for _, s := range sns {
			if !strings.HasPrefix(s.DisplayName, alerting.TestPrefix) {
				continue
			}
			counts["test snoozes found (cannot be deleted by API)"]++
			if s.State == "active" {
				if _, err := sc.End(ctx, s.Name, time.Time{}); err != nil {
					t.Errorf("end leftover snooze: %v", err)
					continue
				}
				counts["snoozes ended"]++
			}
		}
	}

	acct := strings.TrimSpace(os.Getenv("CLOUD0_BILLING_ACCOUNT_ID"))
	if acct == "" {
		t.Fatal("CLOUD0_BILLING_ACCOUNT_ID must be set")
	}
	bc, err := budgets.New(ctx, acct, projects[0], creds.Writer...)
	if err != nil {
		t.Fatal(err)
	}
	bl, err := bc.List(ctx)
	if err != nil {
		t.Fatalf("list budgets: %v", err)
	}
	for _, v := range bl {
		if strings.HasPrefix(v.DisplayName, alerting.TestPrefix) {
			if err := bc.Delete(ctx, v.Name); err != nil {
				t.Errorf("delete leftover budget: %v", err)
				continue
			}
			counts["budgets deleted"]++
		}
	}
	t.Logf("SWEEP RESULT: %s", fmt.Sprint(counts))
}
