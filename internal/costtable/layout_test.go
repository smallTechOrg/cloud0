package costtable

import (
	"strings"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// withLayout installs l for one test and restores the package's sheet layout.
func withLayout(t *testing.T, l Layout) {
	t.Helper()
	prev := ActiveLayout()
	SetLayout(l)
	t.Cleanup(func() { SetLayout(prev) })
}

func keysOf(cols []Column) string {
	k := make([]string, len(cols))
	for i, c := range cols {
		k[i] = c.Key
	}
	return strings.Join(k, ",")
}

func TestDynamicLayout_ColumnsFollowTheDataOrderedByNet(t *testing.T) {
	withLayout(t, DynamicLayout())
	s42 := snap(12, w42, isoweek.StatusFinal, week42Rows())
	tbl := assemble(t, baseInput(map[isoweek.Week]store.Snapshot{w42: s42}, map[int64][]store.SnapshotRow{12: week42Rows()}))

	// Net: Compute Engine 1100, Networking 403.41, Cloud SQL 206.17, Cloud Monitoring 10,
	// Gemini API 1.5, Generative Language API 0.98; VM Manager and Cloud Debugger are
	// fully credited (net 0, gross > 0) and still shown, last, by label.
	want := "week,svc:Compute Engine,svc:Networking,svc:Cloud SQL,svc:Cloud Monitoring,svc:Gemini API,svc:Generative Language API," +
		"svc:Cloud Debugger,svc:VM Manager,total,pct_change"
	if got := keysOf(tbl.Columns); got != want {
		t.Fatalf("columns =\n%s\nwant\n%s", got, want)
	}
	for _, c := range tbl.Columns {
		if c.Extra || c.Kind == KindStub {
			t.Errorf("dynamic layout marks %q extra=%v kind=%s", c.Key, c.Extra, c.Kind)
		}
	}
	cells := tbl.Rows[1].Cells
	var sum float64
	for _, c := range tbl.Columns {
		if c.Kind == KindService {
			sum += cells[c.Key].Net
		}
	}
	if !near(cells[KeyTotal].Net, sum) || !near(cells[KeyTotal].Net, s42.NetTotal) {
		t.Errorf("total %v, Σ cells %v, snapshot net %v", cells[KeyTotal].Net, sum, s42.NetTotal)
	}
}

func TestDynamicLayout_NoDataMeansNoServiceColumns(t *testing.T) {
	withLayout(t, DynamicLayout())
	if got := keysOf(ColumnsFor(nil)); got != "week,total,pct_change" {
		t.Errorf("columns = %s", got)
	}
	if got := keysOf(ColumnsFor(map[string]ServiceTotal{"BigQuery": {}})); got != "week,total,pct_change" {
		t.Errorf("an all-zero service got a column: %s", got)
	}
}

func TestParseLayout_PinnedColumnsAliasesKeysAndVendors(t *testing.T) {
	l, err := ParseLayout(" Compute Engine ; db:Database=Cloud SQL|AlloyDB ;; GCS=Cloud Storage", "groq:AI Cost (Groq)")
	if err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{"compute engine": "compute_engine", "cloud sql": "db", "AlloyDB": "db", "Cloud Storage": "gcs", "GCS": "svc:GCS", "BigQuery": "svc:BigQuery"} {
		if got := l.KeyFor(in); got != want {
			t.Errorf("KeyFor(%q) = %q, want %q", in, got, want)
		}
	}
	cols := l.Columns(map[string]ServiceTotal{"Cloud SQL": {Net: 5}, "Zeta": {Net: 1}, "Alpha": {Net: 9}, "Free": {Gross: 3}})
	if got := keysOf(cols); got != "week,compute_engine,db,gcs,svc:Alpha,svc:Zeta,total,pct_change,stub:groq" {
		t.Fatalf("columns = %s", got)
	}
	if cols[2].Label != "Database" || strings.Join(cols[2].Services, "|") != "Cloud SQL|AlloyDB" || !cols[4].Extra || cols[8].Kind != KindStub || cols[8].Label != "AI Cost (Groq)" {
		t.Errorf("column metadata wrong: %+v", cols)
	}
}

func TestParseLayout_EmptyIsDynamicAndBadInputNamesTheVariable(t *testing.T) {
	l, err := ParseLayout("  ", "")
	if err != nil || l.Pinned() || len(l.vendors) != 0 {
		t.Fatalf("empty spec = %+v, %v", l, err)
	}
	bad := map[string][2]string{
		"duplicate key": {"Cloud Run; Cloud Run", ""},
		"service twice": {"a:A=Cloud Run; b:B=cloud run", ""},
		"reserved key":  {"total:Total", ""},
		"bad key":       {"Bad Key:Label", ""},
		"no services":   {"Label=", ""},
		"no label":      {"key:", ""},
		"bad vendor":    {"", "Bad!:x"},
	}
	for name, in := range bad {
		_, err := ParseLayout(in[0], in[1])
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		want := "CLOUD0_COST_COLUMNS"
		if in[1] != "" {
			want = "CLOUD0_VENDOR_COLUMNS"
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %q does not name %s", name, err, want)
		}
	}
}

func TestSheetLayout_ReproducesTheOriginalSheetHeader(t *testing.T) {
	l := MustParseLayout(SheetColumns, SheetVendors)
	labels := []string{}
	for _, c := range l.Columns(nil) {
		labels = append(labels, c.Label)
	}
	want := "Week,Compute Engine,Networking,CloudSQL,Gemini API,Cloud Run,GCS,VM manager,Cloud Build,Cloud Logging,Secret Manager,Artifact Registry,Total GCP,% change,AI Cost (Groq),claude (usage)"
	if strings.Join(labels, ",") != want {
		t.Errorf("labels = %s", strings.Join(labels, ","))
	}
}
