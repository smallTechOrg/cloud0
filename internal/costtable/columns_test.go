package costtable

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestColumnKeyFor_MapsSheetServicesAndFoldsCaseAndSpace(t *testing.T) {
	cases := map[string]string{
		"Compute Engine":          "compute_engine",
		"Networking":              "networking",
		"Cloud SQL":               "cloud_sql",
		"cloud sql ":              "cloud_sql",
		"  CLOUD SQL":             "cloud_sql",
		"Gemini API":              "gemini_api",
		"Generative Language API": "gemini_api",
		"Cloud Run":               "cloud_run",
		"Cloud Storage":           "gcs",
		"VM Manager":              "vm_manager",
		"Cloud Build":             "cloud_build",
		"Cloud Logging":           "cloud_logging",
		"Secret Manager":          "secret_manager",
		"Artifact Registry":       "artifact_registry",
		"Cloud Monitoring":        "svc:Cloud Monitoring",
		" Cloud Monitoring ":      "svc:Cloud Monitoring",
		"BigQuery":                "svc:BigQuery",
	}
	for in, want := range cases {
		if got := ColumnKeyFor(in); got != want {
			t.Errorf("ColumnKeyFor(%q) = %q, want %q", in, got, want)
		}
	}
	if !IsExtraKey("svc:BigQuery") || IsExtraKey("gcs") || IsExtraKey("total") {
		t.Error("IsExtraKey wrong")
	}
}

func TestColumns_FixedOrderExtrasSortedBeforeTotalStubsLast(t *testing.T) {
	cols := Columns([]string{"Cloud Monitoring", "BigQuery"})
	var keys []string
	for _, c := range cols {
		keys = append(keys, c.Key)
	}
	want := []string{
		"week", "compute_engine", "networking", "cloud_sql", "gemini_api", "cloud_run", "gcs", "vm_manager",
		"cloud_build", "cloud_logging", "secret_manager", "artifact_registry",
		"svc:BigQuery", "svc:Cloud Monitoring",
		"total", "pct_change", "stub:groq", "stub:claude",
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys =\n%v\nwant\n%v", keys, want)
	}
	labels := []string{}
	for _, c := range cols {
		labels = append(labels, c.Label)
	}
	wantLabels := "Week,Compute Engine,Networking,CloudSQL,Gemini API,Cloud Run,GCS,VM manager,Cloud Build,Cloud Logging,Secret Manager,Artifact Registry,BigQuery,Cloud Monitoring,Total GCP,% change,AI Cost (Groq),claude (usage)"
	if strings.Join(labels, ",") != wantLabels {
		t.Fatalf("labels = %s", strings.Join(labels, ","))
	}
	// Kinds and metadata.
	if cols[0].Kind != KindWeek || cols[1].Kind != KindService || cols[14].Kind != KindTotal || cols[15].Kind != KindPctChange || cols[16].Kind != KindStub || cols[17].Kind != KindStub {
		t.Errorf("kinds wrong: %+v", cols)
	}
	if cols[4].Services[0] != "Gemini API" || cols[4].Services[1] != "Generative Language API" || cols[6].Services[0] != "Cloud Storage" {
		t.Errorf("services lists wrong: %+v %+v", cols[4], cols[6])
	}
	if !cols[12].Extra || cols[12].Services[0] != "BigQuery" || cols[1].Extra {
		t.Errorf("extra flag wrong: %+v / %+v", cols[12], cols[1])
	}
	if cols[16].Phase != 4 || cols[17].Phase != 4 || cols[1].Phase != 0 {
		t.Errorf("stub phase wrong")
	}
	// No extras → fixed header only.
	if n := len(Columns(nil)); n != 16 {
		t.Errorf("Columns(nil) has %d columns, want 16", n)
	}
	// Duplicate / whitespace extras collapse.
	if n := len(Columns([]string{"BigQuery", " BigQuery", "BigQuery "})); n != 17 {
		t.Errorf("duplicate extras not collapsed: %d", n)
	}
}

func TestColumn_JSONOmitsIrrelevantFields(t *testing.T) {
	cols := Columns([]string{"Cloud Monitoring"})
	b, err := json.Marshal(cols)
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	week, svc, extra, total, stub := got[0], got[1], got[12], got[13], got[15]
	if len(week) != 3 || string(week["kind"]) != `"week"` {
		t.Errorf("week column = %v", week)
	}
	if _, ok := svc["services"]; !ok || len(svc) != 4 {
		t.Errorf("service column = %v", svc)
	}
	if string(extra["extra"]) != "true" || string(extra["key"]) != `"svc:Cloud Monitoring"` || len(extra) != 5 {
		t.Errorf("extra column = %v", extra)
	}
	if len(total) != 3 || string(total["label"]) != `"Total GCP"` {
		t.Errorf("total column = %v", total)
	}
	if string(stub["phase"]) != "4" || len(stub) != 4 {
		t.Errorf("stub column = %v", stub)
	}
}
