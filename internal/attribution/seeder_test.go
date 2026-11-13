package attribution

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

func crow(project, service, sku, name string, net float64, labels map[string]string) CostRow {
	return CostRow{Row: Row{ProjectID: project, Service: service, SKU: sku, ResourceName: name, Labels: labels}, Gross: net, Net: net, LineItems: 1}
}

// twoProducts: product-a and product-b with prod/staging variants plus noise.
func twoProducts() []CostRow {
	return []CostRow{
		crow("proj-1", "Compute Engine", "core", "vm-product-a-prod", 100, nil),
		crow("proj-1", "Cloud Run", "cpu", "svc-product-a-web-prod", 50, nil),
		crow("proj-1", "Cloud Storage", "std", "bucket-product-a-media-prod", 10, nil),
		crow("proj-1", "Cloud Run", "cpu", "svc-product-a-web-staging", 20, nil),
		crow("proj-2", "Compute Engine", "core", "vm-product-b-prod", 80, nil),
		crow("proj-2", "Cloud Run", "cpu", "svc-product-b-web-prod", 40, nil),
		crow("proj-2", "Cloud Storage", "std", "bucket-product-b-media", 5, nil),
		crow("proj-2", "Compute Engine", "core", "vm-backend-1", 2, nil),
		crow("proj-2", "Compute Engine", "core", "vm-backend-2", 2, nil),
		crow("proj-1", "Networking", "Network Load Balancing Forwarding Rule Minimum Service Charge", "", 30, nil),
		crow("proj-1", "Compute Engine", "Static IP", "", 1, nil),
	}
}

func seedOf(t *testing.T, rows []CostRow, opts SeedOptions) SeedAnalysis {
	t.Helper()
	a, err := AnalyzeSeed(rows, opts)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func findRule(a SeedAnalysis, field, value string) (Rule, bool) {
	for _, r := range a.Result.Proposed {
		if r.Match.Field == field && strings.Contains(strings.ToLower(r.Match.Value), strings.ToLower(value)) {
			return r, true
		}
	}
	return Rule{}, false
}

func products(a SeedAnalysis) map[string]bool {
	m := map[string]bool{}
	for _, r := range a.Result.Proposed {
		if r.Action.Product != "" {
			m[r.Action.Product] = true
		}
	}
	return m
}

func TestSeedTwoProductsWithEnvironmentsAndSharedLB(t *testing.T) {
	a := seedOf(t, twoProducts(), SeedOptions{})
	p := products(a)
	if !p["product-a"] || !p["product-b"] || len(p) != 2 {
		t.Fatalf("products = %v, want exactly product-a and product-b", p)
	}
	if p["product"] {
		t.Fatal("shared prefix 'product' must not become a product")
	}
	envs := map[string]bool{}
	for _, r := range a.Result.Proposed {
		if r.Action.Environment != "" {
			envs[r.Action.Environment] = true
		}
		if r.ID != nil || r.Source != SourceSeed || !r.Enabled || r.Note == nil {
			t.Errorf("bad rule shape %+v", r)
		}
	}
	if !envs["prod"] || !envs["staging"] || len(envs) != 2 {
		t.Fatalf("envs = %v", envs)
	}
	lb, ok := findRule(a, FieldSKU, "forwarding rule")
	if !ok || !lb.Action.Shared || lb.Priority < TierSharedMin || lb.Priority > TierSharedMax {
		t.Fatalf("forwarding-rule shared rule missing/misplaced: %+v ok=%v", lb, ok)
	}
	// total = 340; attributed = 305 by tokens + 4 backend VMs via the proj-2 project rule = 309;
	// shared = 30 (LB); unattributed = 1 (static IP in proj-1, which has shared rows so no project rule).
	c := a.CoverageAfter
	if math.Abs(c.TotalNet-340) > 1e-9 || math.Abs(c.AttributedNet-309) > 1e-9 || math.Abs(c.SharedNet-30) > 1e-9 || math.Abs(c.UnattributedNet-1) > 1e-9 {
		t.Fatalf("coverage = %+v", c)
	}
	if c.AttributedPct != 90.88 || c.SharedPct != 8.82 || math.Abs(c.AttributedPct+c.SharedPct+c.UnattributedPct-100) > 0.011 {
		t.Fatalf("pcts = %+v", c)
	}
	if b := a.CoverageBefore; b.AttributedPct != 0 || b.UnattributedPct != 100 {
		t.Fatalf("before = %+v", b)
	}
	// explanations name the resources
	r, _ := findRule(a, FieldResourceName, "product[-_.]?a")
	if r.Note == nil || !strings.Contains(*r.Note, `token "product-a" appears on 4 resources`) || !strings.Contains(*r.Note, "vm-product-a-prod") {
		t.Fatalf("note = %v", r.Note)
	}
	if len(a.RuleStats) != len(a.Result.Proposed) {
		t.Fatal("RuleStats must be parallel to Proposed")
	}
	// product summaries
	if len(a.Result.Products) != 2 || a.Result.Products[0].Product != "product-a" || a.Result.Products[0].Net != 180 || a.Result.Products[0].Resources != 4 {
		t.Fatalf("products summary = %+v", a.Result.Products)
	}
	if len(a.Unmatched) != 1 || a.Unmatched[0].SKU != "Static IP" || a.Unmatched[0].Net != 1 {
		t.Fatalf("unmatched not sorted: %+v", a.Unmatched)
	}
	n := a.Result.Counts
	if n.Rules != len(a.Result.Proposed) || n.Products != 2 || n.Environments != 2 || n.SKUService < 1 || n.ResourceName < 4 || n.Project != 1 {
		t.Fatalf("counts = %+v", n)
	}
}

func TestSeedStopTokensNeverBecomeProducts(t *testing.T) {
	var rows []CostRow
	for _, n := range []string{"vm-backend-1", "svc-backend-api", "bucket-media-backups", "db-cache-1", "vm-frontend-2", "storefront-web", "lb-default-ip", "migrate-jobs", "repo-instances"} {
		rows = append(rows, crow("proj-1", "Compute Engine", "core", n, 100, nil))
	}
	a := seedOf(t, rows, SeedOptions{})
	if len(products(a)) != 0 {
		t.Fatalf("products invented from generic tokens: %v", products(a))
	}
	if a.CoverageAfter.AttributedPct != 0 {
		t.Fatalf("coverage = %+v", a.CoverageAfter)
	}
}

func TestSeedSingleResourceThreshold(t *testing.T) {
	rows := []CostRow{
		crow("proj-1", "Compute Engine", "core", "vm-bigone-prod", 100, nil),
		crow("proj-1", "Compute Engine", "core", "vm-tinything-1", 1, nil),
		crow("proj-1", "Compute Engine", "core", "vm-other-2", 1, nil),
	}
	a := seedOf(t, rows, SeedOptions{})
	p := products(a)
	if !p["bigone"] {
		t.Fatalf("single resource with >=5%% share should qualify: %v", p)
	}
	if p["tinything"] || p["other"] {
		t.Fatalf("single low-cost resource tokens must be ignored: %v", p)
	}
}

func TestSeedAliasesAndLabels(t *testing.T) {
	rows := []CostRow{
		crow("proj-1", "Compute Engine", "core", "vm-one", 40, map[string]string{"app": "0", "env": "prod"}),
		crow("proj-1", "Compute Engine", "core", "vm-two", 10, map[string]string{"app": "0"}),
		crow("proj-1", "Compute Engine", "core", "vm-three", 20, map[string]string{"app": "Product-B"}),
		crow("proj-1", "Compute Engine", "core", "vm-four", 5, map[string]string{"product": "Product-C"}),
		crow("proj-1", "Compute Engine", "core", "vm-five", 5, map[string]string{"product": "shared"}),
		crow("proj-1", "Compute Engine", "core", "vm-six", 2, nil),
	}
	a := seedOf(t, rows, SeedOptions{Aliases: map[string]string{"0": "zero"}})
	r, ok := findRule(a, FieldLabel, "0")
	if !ok || r.Action.Product != "zero" || r.Match.Key != "app" || r.Priority < TierLabelMin || r.Priority > TierLabelMax {
		t.Fatalf("alias label rule = %+v ok=%v", r, ok)
	}
	if _, ok := findRule(a, FieldLabel, "product-b"); !ok {
		t.Fatal("app label rule product-b missing (values are lower-cased)")
	}
	if a.Result.Counts.Label != 2 { // product=Product-C equals the built-in label rule and is skipped
		t.Fatalf("label count = %d", a.Result.Counts.Label)
	}
	// vm-one,vm-two->zero(50) vm-three->product-b(20) vm-four->product-c(5) builtin; vm-five shared(5);
	// vm-six (2 of 82 net) is below the single-resource share threshold -> unattributed
	c := a.CoverageAfter
	if c.TotalNet != 82 || c.AttributedNet != 75 || c.SharedNet != 5 || c.UnattributedNet != 2 {
		t.Fatalf("coverage = %+v", c)
	}
	// before: only the built-in product label applies
	if b := a.CoverageBefore; b.AttributedNet != 5 || b.SharedNet != 5 {
		t.Fatalf("before = %+v", b)
	}
}

func TestSeedAliasOnNameToken(t *testing.T) {
	rows := []CostRow{
		crow("proj-1", "Compute Engine", "core", "vm-alias-b-prod", 10, nil),
		crow("proj-1", "Compute Engine", "core", "svc-alias-b-web", 10, nil),
		crow("proj-2", "Compute Engine", "core", "vm-unrelated", 1, nil),
	}
	a := seedOf(t, rows, SeedOptions{Aliases: map[string]string{"alias-b": "example-product"}})
	r, ok := findRule(a, FieldResourceName, "alias")
	if !ok || r.Action.Product != "example-product" || r.Priority < TierNameMin || r.Priority > TierNameMax {
		t.Fatalf("alias name rule = %+v ok=%v", r, ok)
	}
	if products(a)["alias-b"] {
		t.Fatal("aliased token must not also become its own product")
	}
	if a.CoverageAfter.AttributedNet != 20 || a.CoverageAfter.UnattributedNet != 1 {
		t.Fatalf("coverage = %+v", a.CoverageAfter)
	}
}

func TestSeedSharedTokensTakePrecedence(t *testing.T) {
	rows := []CostRow{
		crow("proj-1", "Cloud SQL", "db", "gamma-shared-db", 50, nil),
		crow("proj-1", "Compute Engine", "core", "vm-gamma-1", 30, nil),
		crow("proj-1", "Compute Engine", "core", "svc-gamma-web", 20, nil),
	}
	a := seedOf(t, rows, SeedOptions{SharedTokens: []string{"Shared-DB"}})
	r, ok := findRule(a, FieldResourceName, "shared-db")
	if !ok || !r.Action.Shared {
		t.Fatalf("shared token rule missing: %+v", r)
	}
	c := a.CoverageAfter
	if c.SharedNet != 50 || c.AttributedNet != 50 {
		t.Fatalf("coverage = %+v", c)
	}
}

func TestSeedProjectRule(t *testing.T) {
	rows := []CostRow{
		crow("proj-g", "Compute Engine", "core", "vm-gamma-prod", 50, nil),
		crow("proj-g", "Cloud Run", "cpu", "svc-gamma-web", 30, nil),
		crow("proj-g", "Networking", "egress", "", 20, nil),
		// a project with mixed products gets no project rule
		crow("proj-m", "Compute Engine", "core", "vm-gamma-2", 10, nil),
		crow("proj-m", "Compute Engine", "core", "vm-delta-1", 10, nil),
		crow("proj-m", "Compute Engine", "core", "svc-delta-web", 10, nil),
		crow("proj-m", "Networking", "egress", "", 5, nil),
	}
	a := seedOf(t, rows, SeedOptions{})
	var proj []Rule
	for _, r := range a.Result.Proposed {
		if r.Match.Field == FieldProject {
			proj = append(proj, r)
		}
	}
	if len(proj) != 1 || proj[0].Match.Value != "proj-g" || proj[0].Action.Product != "gamma" || proj[0].Priority < TierProjectMin || proj[0].Priority > TierProjectMax {
		t.Fatalf("project rules = %+v", proj)
	}
	if a.Result.Counts.Project != 1 {
		t.Fatalf("counts = %+v", a.Result.Counts)
	}
	if a.CoverageAfter.UnattributedNet != 5 {
		t.Fatalf("coverage = %+v", a.CoverageAfter)
	}
}

func TestSeedExistingRules(t *testing.T) {
	user := Rule{ID: ptr64(7), Priority: 50, Match: Match{Field: FieldProject, Op: OpEquals, Value: "proj-2"}, Action: Action{Product: "product-b"}, Enabled: true, Source: SourceUser}
	old := Rule{ID: ptr64(8), Priority: 310, Match: Match{Field: FieldResourceName, Op: OpContains, Value: "product-a"}, Action: Action{Product: "product-a"}, Enabled: true, Source: SourceSeed}
	a := seedOf(t, twoProducts(), SeedOptions{Existing: []Rule{user, old}})
	n := a.Result.Counts
	if n.KeptUserRules != 1 || n.ReplacedSeedRules != 1 {
		t.Fatalf("counts = %+v", n)
	}
	// before: user rule attributes all of proj-2 (80+40+5+2+2) + old seed rule product-a (100+50+10+20)
	if b := a.CoverageBefore; b.AttributedNet != 129+180 {
		t.Fatalf("before = %+v", b)
	}
	for _, r := range a.Result.Proposed {
		if r.Source != SourceSeed {
			t.Fatalf("non-seed rule proposed: %+v", r)
		}
	}
	if a.CoverageAfter.AttributedNet < 309 {
		t.Fatalf("after = %+v", a.CoverageAfter)
	}
}

func ptr64(v int64) *int64 { return &v }

func TestSeedIdempotentAndEmpty(t *testing.T) {
	rows := twoProducts()
	r1, err := NewSeeder().Seed(rows, SeedOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// feed the output back as stored seed rules: same proposal
	var stored []Rule
	for i, r := range r1.Proposed {
		r.ID = ptr64(int64(i + 1))
		stored = append(stored, r)
	}
	r2, _ := NewSeeder().Seed(rows, SeedOptions{Existing: stored})
	if !reflect.DeepEqual(r1.Proposed, r2.Proposed) || !reflect.DeepEqual(r1.Products, r2.Products) {
		t.Fatal("seeding is not idempotent")
	}
	if r2.Counts.ReplacedSeedRules != len(stored) {
		t.Fatalf("counts = %+v", r2.Counts)
	}
	e, err := NewSeeder().Seed(nil, SeedOptions{})
	if err != nil || e.Proposed == nil || e.Products == nil || len(e.Proposed) != 0 {
		t.Fatalf("empty = %+v err=%v", e, err)
	}
}

func TestSeedDeterministicUnderShuffle(t *testing.T) {
	rows := append(twoProducts(),
		crow("proj-g", "Compute Engine", "core", "vm-gamma-prod", 50, map[string]string{"app": "0"}),
		crow("proj-g", "Cloud Run", "cpu", "svc-gamma-web", 50, nil),
		crow("proj-g", "VM Manager", "x", "", 3, nil))
	opts := SeedOptions{Aliases: map[string]string{"0": "zero"}, SharedTokens: []string{"backend"}}
	want := seedOf(t, rows, opts)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		sh := append([]CostRow(nil), rows...)
		rng.Shuffle(len(sh), func(a, b int) { sh[a], sh[b] = sh[b], sh[a] })
		got := seedOf(t, sh, opts)
		if !reflect.DeepEqual(want.Result, got.Result) || want.CoverageAfter != got.CoverageAfter || !reflect.DeepEqual(want.Unmatched, got.Unmatched) {
			t.Fatalf("shuffle %d changed output", i)
		}
	}
}

func TestSeedPerformance50k(t *testing.T) {
	var rows []CostRow
	for i := 0; i < 50000; i++ {
		prod := fmt.Sprintf("prodx%c", 'a'+rune(i%20))
		rows = append(rows, crow(fmt.Sprintf("proj-%d", i%5), "Compute Engine", fmt.Sprintf("sku-%d", i%7),
			fmt.Sprintf("vm-%s-%s-%d", prod, []string{"prod", "staging", "dev"}[i%3], i%500), float64(i%13)+0.5, nil))
	}
	start := time.Now()
	a := seedOf(t, rows, SeedOptions{})
	d := time.Since(start)
	if d > time.Second {
		t.Fatalf("seeding 50k rows took %v", d)
	}
	if a.CoverageAfter.AttributedPct < 99 {
		t.Fatalf("coverage = %+v", a.CoverageAfter)
	}
	t.Logf("50k rows in %v, %d rules", d, len(a.Result.Proposed))
}

func TestSeedValidRules(t *testing.T) {
	a := seedOf(t, twoProducts(), SeedOptions{})
	for _, r := range a.Result.Proposed {
		if r.Match.Value == "" {
			t.Errorf("empty value: %+v", r)
		}
		if r.Action.Product != "" && !seedNameRe.MatchString(r.Action.Product) {
			t.Errorf("invalid product name %q", r.Action.Product)
		}
		if r.Action.Shared && r.Action.Product != "" {
			t.Errorf("shared+product: %+v", r)
		}
	}
}
