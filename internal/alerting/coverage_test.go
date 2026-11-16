package alerting

import (
	"context"
	"strings"
	"testing"
)

type fakeCov struct {
	vms    []VMInfo
	dbs    []DBInfo
	runs   []ServiceInfo
	pols   []PolicyView
	agents map[string]bool
}

func (f fakeCov) VMs(context.Context) ([]VMInfo, error)           { return f.vms, nil }
func (f fakeCov) CloudSQL(context.Context) ([]DBInfo, error)      { return f.dbs, nil }
func (f fakeCov) CloudRun(context.Context) ([]ServiceInfo, error) { return f.runs, nil }
func (f fakeCov) Policies(context.Context) ([]PolicyView, error)  { return f.pols, nil }
func (f fakeCov) HasOpsAgent(_ context.Context, vm VMInfo) (bool, error) {
	return f.agents[vm.Name], nil
}

func covPolicy(name, filter, typ string) PolicyView {
	return PolicyView{Name: "projects/proj-a/alertPolicies/" + name, Project: "proj-a", Enabled: true,
		Conditions: []ConditionSummary{{Type: typ, Filter: filter}}}
}

func covRow(t *testing.T, rows []CoverageRow, typ, res string) CoverageRow {
	t.Helper()
	n := 0
	var out CoverageRow
	for _, r := range rows {
		if r.ResourceType == typ && r.Resource == res {
			out = r
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%s %s appears %d times", typ, res, n)
	}
	return out
}

func covSig(r CoverageRow, name string) CoverageSignal {
	for _, s := range r.Signals {
		if s.Signal == name {
			return s
		}
	}
	return CoverageSignal{Signal: "MISSING"}
}

func TestCoverageVMGapHints(t *testing.T) {
	src := fakeCov{
		vms: []VMInfo{{Project: "proj-a", Name: "vm-a", ID: "111"}, {Project: "proj-a", Name: "vm-b", ID: "222"}, {Project: "proj-a", Name: "vm-c", ID: "333"}},
		pols: []PolicyView{
			covPolicy("1", `metric.type="compute.googleapis.com/instance/cpu/utilization" resource.type="gce_instance"`, "threshold"),
			covPolicy("2", `metric.type="agent.googleapis.com/memory/percent_used" resource.type="gce_instance"`, "threshold"),
		},
		agents: map[string]bool{"vm-b": true, "vm-c": true},
	}
	rows, err := NewCoverageProvider(src).Coverage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := covRow(t, rows, "vm", "vm-a")
	if !covSig(a, "cpu").Covered || covSig(a, "memory").Covered || covSig(a, "disk").Covered ||
		a.Hint != "VM vm-a has CPU alert but no memory/disk alert coverage: install the Ops Agent." {
		t.Fatalf("vm-a: %+v", a)
	}
	if !strings.Contains(covSig(a, "memory").Reason, "Ops Agent") {
		t.Fatalf("reason: %+v", covSig(a, "memory"))
	}
	b := covRow(t, rows, "vm", "vm-b")
	if !covSig(b, "memory").Covered || covSig(b, "disk").Covered || !strings.Contains(b.Hint, "disk") ||
		covSig(b, "memory").Policies[0] != "projects/proj-a/alertPolicies/2" {
		t.Fatalf("vm-b: %+v", b)
	}
}

func TestCoverageSingleVMPolicyAndDisabled(t *testing.T) {
	src := fakeCov{
		vms: []VMInfo{{Project: "proj-a", Name: "vm-a", ID: "111"}, {Project: "proj-a", Name: "vm-b", ID: "222"}},
		pols: []PolicyView{
			covPolicy("1", `metric.type="compute.googleapis.com/instance/cpu/utilization" metric.labels.instance_name="vm-a"`, "threshold"),
		},
	}
	off := covPolicy("2", `metric.type="compute.googleapis.com/instance/cpu/utilization"`, "threshold")
	off.Enabled = false
	src.pols = append(src.pols, off)
	rows, _ := NewCoverageProvider(src).Coverage(context.Background())
	if !covSig(covRow(t, rows, "vm", "vm-a"), "cpu").Covered {
		t.Fatal("vm-a should be covered by its own policy")
	}
	b := covRow(t, rows, "vm", "vm-b")
	if covSig(b, "cpu").Covered || !strings.Contains(b.Hint, "no CPU alert") {
		t.Fatalf("vm-b must not be covered by a vm-a-only or disabled policy: %+v", b)
	}
}

func TestCoverageCloudRunAndSQL(t *testing.T) {
	src := fakeCov{
		runs: []ServiceInfo{{Project: "proj-a", Name: "svc-a"}, {Project: "proj-a", Name: "svc-b"}},
		dbs:  []DBInfo{{Project: "proj-a", Name: "db-a", Tier: "db-f1-micro"}, {Project: "proj-a", Name: "db-b", Tier: "db-custom-1-3840"}},
		pols: []PolicyView{
			covPolicy("1", `metric.type="run.googleapis.com/request_count" resource.type="cloud_run_revision" metric.labels.response_code_class="5xx" resource.labels.service_name="svc-a"`, "threshold"),
			covPolicy("2", `metric.type="cloudsql.googleapis.com/database/cpu/utilization" resource.type="cloudsql_database"`, "threshold"),
			covPolicy("3", `metric.type="cloudsql.googleapis.com/database/up" resource.labels.database_id="proj-a:db-b"`, "threshold"),
		},
	}
	rows, err := NewCoverageProvider(src).Coverage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !covSig(covRow(t, rows, "cloud_run", "svc-a"), "errors").Covered {
		t.Fatal("svc-a should be covered")
	}
	b := covRow(t, rows, "cloud_run", "svc-b")
	if covSig(b, "errors").Covered || !strings.Contains(b.Hint, "svc-b") {
		t.Fatalf("svc-b: %+v", b)
	}
	a := covRow(t, rows, "cloudsql", "db-a")
	if covSig(a, "down").Covered || !strings.Contains(a.Hint, "no database-down alert") ||
		!covSig(a, "cpu").Covered || !strings.Contains(covSig(a, "memory").Reason, "Shared-core") {
		t.Fatalf("db-a: %+v", a)
	}
	d := covRow(t, rows, "cloudsql", "db-b")
	if !covSig(d, "down").Covered || strings.Contains(d.Hint, "down") {
		t.Fatalf("db-b: %+v", d)
	}
}

func TestCoverageLogMatchCoversCloudRun(t *testing.T) {
	src := fakeCov{
		runs: []ServiceInfo{{Project: "proj-a", Name: "svc-a"}},
		pols: []PolicyView{covPolicy("1", `resource.type="cloud_run_revision" AND severity>=ERROR`, "log_match")},
	}
	rows, _ := NewCoverageProvider(src).Coverage(context.Background())
	if !covSig(covRow(t, rows, "cloud_run", "svc-a"), "errors").Covered {
		t.Fatal("log-match should cover")
	}
}

func TestCoverageEmpty(t *testing.T) {
	rows, err := NewCoverageProvider(fakeCov{}).Coverage(context.Background())
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("%v %#v", err, rows)
	}
}
