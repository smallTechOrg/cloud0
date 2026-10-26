package monclient

import (
	"context"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/alerting"
	"github.com/smallTechOrg/cloud0/internal/testenv"
)

func TestInvMapErrNonGoogleError(t *testing.T) {
	err := invMapErr(context.DeadlineExceeded, "x", "proj-a", "roles/x")
	if err != context.DeadlineExceeded {
		t.Fatalf("context errors must pass through, got %v", err)
	}
	if invMapErr(nil, "x", "p", "r") != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestInventoryRealAndCoverage(t *testing.T) {
	testenv.LoadDotEnv(t)
	projects := incRealProjects(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pc, err := NewPolicies(ctx, Creds{})
	if err != nil {
		t.Fatalf("NewPolicies: %v", err)
	}
	inv, err := NewInventory(ctx, projects, Creds{}, pc)
	if err != nil {
		t.Fatalf("NewInventory: %v", err)
	}
	defer inv.Close()

	vms, err := inv.VMs(ctx)
	if err != nil {
		t.Fatalf("VMs: %v", err)
	}
	if len(vms) < 1 {
		t.Fatal("want >= 1 VM")
	}
	dbs, err := inv.CloudSQL(ctx)
	if err != nil {
		t.Fatalf("CloudSQL: %v", err)
	}
	if len(dbs) < 1 {
		t.Fatal("want >= 1 Cloud SQL instance")
	}
	runs, err := inv.CloudRun(ctx)
	if err != nil {
		t.Fatalf("CloudRun: %v", err)
	}
	t.Logf("inventory: %d VMs, %d Cloud SQL, %d Cloud Run", len(vms), len(dbs), len(runs))

	withAgent := 0
	for _, vm := range vms {
		ok, err := inv.HasOpsAgent(ctx, vm)
		if err != nil {
			t.Fatalf("HasOpsAgent: %v", err)
		}
		if ok {
			withAgent++
		}
	}
	t.Logf("%d of %d VMs report the Ops Agent", withAgent, len(vms))

	rows, err := alerting.NewCoverageProvider(inv).Coverage(ctx)
	if err != nil {
		t.Fatalf("Coverage: %v", err)
	}
	seen := map[string]int{}
	for _, r := range rows {
		if r.ResourceType == "vm" {
			seen[r.Project+"/"+r.Resource]++
		}
		if len(r.Signals) == 0 {
			t.Fatalf("row without signals: %+v", r)
		}
	}
	for _, vm := range vms {
		if n := seen[vm.Project+"/"+vm.Name]; n != 1 {
			t.Fatalf("VM #%s referenced %d times, want exactly once", vm.ID, n)
		}
	}
	if len(rows) != len(vms)+len(dbs)+len(runs) {
		t.Fatalf("rows %d != inventory %d", len(rows), len(vms)+len(dbs)+len(runs))
	}
}
