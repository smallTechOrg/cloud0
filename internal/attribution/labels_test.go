package attribution

import (
	"strings"
	"testing"
)

// lsEngine decides by resource name: map name -> Evaluation.
type lsEngine struct {
	evals map[string]Evaluation
	rules []Rule
}

func (s lsEngine) Evaluate(r Row) Evaluation {
	if e, ok := s.evals[r.ResourceName]; ok {
		return e
	}
	return Evaluation{}
}
func (s lsEngine) Explain(Row) []TrailStep { return nil }
func (s lsEngine) Rules() []Rule           { return s.rules }

func lsID(v int64) *int64 { return &v }

func lsEval(product, env string, pr, er *int64) Evaluation {
	return Evaluation{Product: product, Environment: env, RuleID: pr, EnvironmentRuleID: er}
}

func lsRow(project, name, global string, net float64) CostRow {
	return CostRow{Row: Row{ProjectID: project, ResourceName: name, ResourceGlobalName: global, Service: "Svc", SKU: "Sku"}, Net: net}
}

func lsSuggest(t *testing.T, rows []CostRow, evals map[string]Evaluation, rules []Rule) ([]LabelSuggestion, int) {
	t.Helper()
	return NewLabelSuggester().Suggest(rows, lsEngine{evals: evals, rules: rules})
}

func TestEveryResourceType(t *testing.T) {
	ev := lsEval("example-product", "prod", lsID(2), lsID(3))
	cases := []struct {
		name, global, rtype, loc, cmd string
	}{
		{"example-vm", "//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-vm", TypeComputeInstance, "zone-a",
			"gcloud compute instances add-labels example-vm --project=proj-a --zone=zone-a --labels=product=example-product,env=prod"},
		{"example-disk", "//compute.googleapis.com/projects/proj-a/zones/zone-a/disks/example-disk", TypeComputeDisk, "zone-a",
			"gcloud compute disks add-labels example-disk --project=proj-a --zone=zone-a --labels=product=example-product,env=prod"},
		{"example-rdisk", "//compute.googleapis.com/projects/proj-a/regions/region-a/disks/example-rdisk", TypeComputeDisk, "region-a",
			"gcloud compute disks add-labels example-rdisk --project=proj-a --region=region-a --labels=product=example-product,env=prod"},
		{"example-ip", "//compute.googleapis.com/projects/proj-a/regions/region-a/addresses/example-ip", TypeComputeAddress, "region-a",
			"gcloud compute addresses add-labels example-ip --project=proj-a --region=region-a --labels=product=example-product,env=prod"},
		{"example-web", "//run.googleapis.com/projects/proj-a/locations/region-a/services/example-web", TypeCloudRunService, "region-a",
			"gcloud run services update example-web --project=proj-a --region=region-a --update-labels=product=example-product,env=prod"},
		{"example-job", "//run.googleapis.com/projects/proj-a/locations/region-a/jobs/example-job", TypeCloudRunJob, "region-a",
			"gcloud run jobs update example-job --project=proj-a --region=region-a --update-labels=product=example-product,env=prod"},
		{"example-bucket", "//storage.googleapis.com/projects/_/buckets/example-bucket", TypeGCSBucket, "",
			"gcloud storage buckets update gs://example-bucket --project=proj-a --update-labels=product=example-product,env=prod"},
		{"example-sql", "//sqladmin.googleapis.com/projects/proj-a/instances/example-sql", TypeCloudSQLInstance, "",
			"gcloud sql instances patch example-sql --project=proj-a --update-labels=product=example-product,env=prod"},
		{"example-repo", "//artifactregistry.googleapis.com/projects/proj-a/locations/region-a/repositories/example-repo", TypeArtifactRepository, "region-a",
			"gcloud artifacts repositories update example-repo --project=proj-a --location=region-a --update-labels=product=example-product,env=prod"},
	}
	for i, c := range cases {
		items, done := lsSuggest(t, []CostRow{lsRow("proj-a", c.name, c.global, float64(100-i))}, map[string]Evaluation{c.name: ev}, nil)
		if done != 0 || len(items) != 1 {
			t.Fatalf("%s: items=%d done=%d", c.name, len(items), done)
		}
		it := items[0]
		if it.ResourceType != c.rtype {
			t.Errorf("%s: type %q want %q", c.name, it.ResourceType, c.rtype)
		}
		if c.loc == "" && it.RegionOrZone != nil || c.loc != "" && (it.RegionOrZone == nil || *it.RegionOrZone != c.loc) {
			t.Errorf("%s: loc %v want %q", c.name, it.RegionOrZone, c.loc)
		}
		if it.Command == nil || *it.Command != c.cmd {
			t.Errorf("%s: command %v want %q", c.name, it.Command, c.cmd)
		}
		if it.Risk == "" || it.Basis.ProductRuleID == nil || *it.Basis.ProductRuleID != 2 || *it.Basis.EnvironmentRuleID != 3 {
			t.Errorf("%s: risk/basis %+v %q", c.name, it.Basis, it.Risk)
		}
		if len(it.Missing) != 2 || it.Missing[0] != "product" || it.Missing[1] != "env" {
			t.Errorf("%s: missing %v", c.name, it.Missing)
		}
	}
	// Risk wording per spec.
	items, _ := lsSuggest(t, []CostRow{lsRow("proj-a", "example-web", cases[4].global, 1)}, map[string]Evaluation{"example-web": ev}, nil)
	if !strings.Contains(items[0].Risk, "new Cloud Run revision") {
		t.Errorf("run risk: %q", items[0].Risk)
	}
}

func TestOnlyMissingDimensionsAndBasis(t *testing.T) {
	g := "//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-vm"
	items, done := lsSuggest(t, []CostRow{lsRow("proj-a", "example-vm", g, 5)},
		map[string]Evaluation{"example-vm": lsEval("example-product", "prod", lsID(2), lsID(0))}, nil)
	if done != 0 || len(items) != 1 {
		t.Fatalf("got %d done %d", len(items), done)
	}
	it := items[0]
	if len(it.Labels) != 1 || it.Labels["product"] != "example-product" || len(it.Missing) != 1 {
		t.Errorf("labels %v missing %v", it.Labels, it.Missing)
	}
	if it.Basis.EnvironmentRuleID != nil || *it.Command != "gcloud compute instances add-labels example-vm --project=proj-a --zone=zone-a --labels=product=example-product" {
		t.Errorf("basis %+v cmd %v", it.Basis, *it.Command)
	}
	// Env only.
	items, _ = lsSuggest(t, []CostRow{lsRow("proj-a", "example-vm", g, 5)},
		map[string]Evaluation{"example-vm": lsEval("", "dev", nil, lsID(7))}, nil)
	if len(items) != 1 || items[0].Labels["env"] != "dev" || items[0].Basis.ProductRuleID != nil {
		t.Errorf("env only: %+v", items)
	}
}

func TestLabelFieldRuleNotSuggested(t *testing.T) {
	rules := []Rule{{ID: lsID(1), Match: Match{Field: FieldLabel, Op: OpEquals, Key: "app", Value: "x"}, Action: Action{Product: "example-product"}}}
	items, done := lsSuggest(t, []CostRow{lsRow("proj-a", "example-vm", "", 5)},
		map[string]Evaluation{"example-vm": lsEval("example-product", "", lsID(1), nil)}, rules)
	if len(items) != 0 || done != 0 {
		t.Errorf("label-field rule suggested: %v done %d", items, done)
	}
}

func TestDoneCountingAndExclusions(t *testing.T) {
	rows := []CostRow{
		lsRow("proj-a", "labelled-a", "", 1),
		lsRow("proj-a", "labelled-b", "", 1),
		lsRow("proj-a", "labelled-b", "", 1), // second SKU of same resource: counted once
		lsRow("proj-a", "shared-res", "", 9),
		lsRow("proj-a", "", "", 50),           // no name
		lsRow("proj-a", "unresolved", "", 20), // nothing decided
		lsRow("proj-a", "example-vm", "//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-vm", 3),
	}
	evals := map[string]Evaluation{
		"labelled-a": lsEval("example-product", "prod", lsID(0), lsID(0)),
		"labelled-b": lsEval("example-product", "prod", lsID(0), lsID(0)),
		"shared-res": {Shared: true, Environment: "prod", RuleID: lsID(5), EnvironmentRuleID: lsID(6)},
		"example-vm": lsEval("example-product", "prod", lsID(2), lsID(3)),
	}
	items, done := lsSuggest(t, rows, evals, nil)
	if done != 2 {
		t.Errorf("done = %d, want 2", done)
	}
	if len(items) != 1 || items[0].ResourceName != "example-vm" {
		t.Fatalf("items = %+v", items)
	}
}

func TestDedupeSumsCostAndOrdering(t *testing.T) {
	ev := lsEval("example-product", "prod", lsID(2), lsID(3))
	rows := []CostRow{
		lsRow("proj-a", "vm-b", "", 10),
		lsRow("proj-a", "vm-a", "", 4),
		lsRow("proj-a", "vm-a", "//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/vm-a", 8), // sum 12
		lsRow("proj-a", "vm-c", "", 10),
		lsRow("proj-b", "vm-a", "", 1), // distinct project -> distinct item
	}
	evals := map[string]Evaluation{"vm-a": ev, "vm-b": ev, "vm-c": ev}
	items, _ := lsSuggest(t, rows, evals, nil)
	var got []string
	for _, it := range items {
		got = append(got, it.ProjectID+"/"+it.ResourceName)
	}
	want := []string{"proj-a/vm-a", "proj-a/vm-b", "proj-a/vm-c", "proj-b/vm-a"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v want %v", got, want)
	}
	if items[0].Net != 12 {
		t.Errorf("net %v want 12", items[0].Net)
	}
	// The global name from any SKU row is used.
	if items[0].ResourceGlobalName == nil || items[0].Command == nil {
		t.Errorf("global name not picked up: %+v", items[0])
	}
}

func TestSanitisation(t *testing.T) {
	long := strings.Repeat("a", 80)
	cases := map[string]string{
		"Example Product":  "example-product",
		"A.B/C":            "a-b-c",
		"keep_under-score": "keep_under-score",
		"  Trim ":          "trim",
		long:               strings.Repeat("a", 63),
		"éx":               "-x",
		"":                 "",
	}
	for in, want := range cases {
		if got := sanitizeLabelValue(in); got != want {
			t.Errorf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	// Item is still emitted when sanitising changes the value.
	items, _ := lsSuggest(t, []CostRow{lsRow("proj-a", "example-vm", "//compute.googleapis.com/projects/proj-a/zones/zone-a/instances/example-vm", 1)},
		map[string]Evaluation{"example-vm": lsEval("Example Product", "", lsID(2), nil)}, nil)
	if len(items) != 1 || items[0].Labels["product"] != "example-product" {
		t.Errorf("items %+v", items)
	}
}

func TestShellQuoting(t *testing.T) {
	if got := shellQuote("it's a name"); got != `'it'\''s a name'` {
		t.Errorf("quote: %s", got)
	}
	if shellQuote("ok-name_1.x") != "ok-name_1.x" {
		t.Error("safe name quoted")
	}
	labels := map[string]string{"product": "p"}
	cmd := buildCommand(TypeGCSBucket, "proj-a", "we ird;name", "", "", labels)
	if cmd == nil || *cmd != "gcloud storage buckets update 'gs://we ird;name' --project=proj-a --update-labels=product=p" {
		t.Errorf("bucket cmd: %v", cmd)
	}
	if buildCommand(TypeComputeInstance, "proj-a", "-rf", "z", "", labels) != nil {
		t.Error("flag-like name must not yield a command")
	}
	if buildCommand(TypeComputeInstance, "", "n", "z", "", labels) != nil {
		t.Error("missing project must not yield a command")
	}
}

func TestUnknownZoneNoCommandWithNote(t *testing.T) {
	ev := map[string]Evaluation{"example-vm": lsEval("example-product", "prod", lsID(2), lsID(3))}
	// Service/SKU fallback type, no global name -> zone unknown.
	r := CostRow{Row: Row{ProjectID: "proj-a", ResourceName: "example-vm", Service: "Compute Engine", SKU: "N2 Instance Core running"}, Net: 3}
	items, _ := lsSuggest(t, []CostRow{r}, ev, nil)
	if len(items) != 1 || items[0].ResourceType != TypeComputeInstance {
		t.Fatalf("items %+v", items)
	}
	if items[0].Command != nil || items[0].RegionOrZone != nil || !strings.Contains(items[0].Risk, "Zone could not be determined") {
		t.Errorf("want nil command + note: %+v", items[0])
	}
	// Bucket and SQL need no location and still get a command from the service fallback.
	b := CostRow{Row: Row{ProjectID: "proj-a", ResourceName: "example-bucket", Service: "Cloud Storage", SKU: "Standard Storage"}, Net: 2}
	items, _ = lsSuggest(t, []CostRow{b}, map[string]Evaluation{"example-bucket": lsEval("example-product", "", lsID(2), nil)}, nil)
	if len(items) != 1 || items[0].Command == nil || !strings.Contains(*items[0].Command, "gs://example-bucket") {
		t.Errorf("bucket fallback: %+v", items)
	}
}

func TestFallbackTypes(t *testing.T) {
	cases := []struct{ svc, sku, want string }{
		{"Compute Engine", "Storage PD Capacity", TypeComputeDisk},
		{"Compute Engine", "Static Ip Charge", TypeComputeAddress},
		{"Compute Engine", "N2 Instance Ram running", TypeComputeInstance},
		{"Cloud Run", "CPU Allocation Time", TypeCloudRunService},
		{"Cloud Run", "Job CPU", TypeCloudRunJob},
		{"Cloud SQL", "db-n1 instance", TypeCloudSQLInstance},
		{"Artifact Registry", "Storage", TypeArtifactRepository},
		{"Something Else", "x", TypeUnknown},
	}
	for _, c := range cases {
		if got := typeFromService(c.svc, c.sku); got != c.want {
			t.Errorf("%s/%s = %s, want %s", c.svc, c.sku, got, c.want)
		}
	}
}

func TestUnknownTypeLabelsOnly(t *testing.T) {
	r := CostRow{Row: Row{ProjectID: "proj-a", ResourceName: "thing", ResourceGlobalName: "//other.googleapis.com/projects/proj-a/widgets/thing", Service: "Other"}, Net: 1}
	items, _ := lsSuggest(t, []CostRow{r}, map[string]Evaluation{"thing": lsEval("example-product", "", lsID(2), nil)}, nil)
	if len(items) != 1 || items[0].ResourceType != TypeUnknown || items[0].Command != nil || !strings.Contains(items[0].Risk, "console") || items[0].Labels["product"] != "example-product" {
		t.Errorf("unknown: %+v", items)
	}
}

func TestEmptyAndNilEngine(t *testing.T) {
	items, done := NewLabelSuggester().Suggest(nil, lsEngine{})
	if items == nil || len(items) != 0 || done != 0 {
		t.Errorf("empty: %v %d", items, done)
	}
	items, done = NewLabelSuggester().Suggest([]CostRow{lsRow("p", "n", "", 1)}, nil)
	if items == nil || len(items) != 0 || done != 0 {
		t.Errorf("nil engine: %v %d", items, done)
	}
}
