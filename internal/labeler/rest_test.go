package labeler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGCP serves just enough of the Compute, Cloud Run and GCS APIs.
type fakeGCP struct {
	diskLabels map[string]string
	fp         string
	gotFP      string
	runLabels  map[string]string
	bucket     map[string]string
	runStatus  int
	runBody    string
}

func (f *fakeGCP) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/compute/projects/p/zones/z/disks/d", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"labels": f.diskLabels, "labelFingerprint": f.fp})
	})
	mux.HandleFunc("/compute/projects/p/zones/z/disks/d/setLabels", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Labels           map[string]string `json:"labels"`
			LabelFingerprint string            `json:"labelFingerprint"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.gotFP, f.diskLabels, f.fp = b.LabelFingerprint, b.Labels, "fp2"
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "DONE"})
	})
	mux.HandleFunc("/run/projects/p/locations/r/services/s", func(w http.ResponseWriter, r *http.Request) {
		if f.runStatus != 0 {
			w.WriteHeader(f.runStatus)
			_, _ = w.Write([]byte(f.runBody))
			return
		}
		if r.Method == http.MethodPatch {
			if r.URL.Query().Get("updateMask") != "labels" {
				http.Error(w, "mask", 400)
				return
			}
			var b struct{ Labels map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			f.runLabels = b.Labels
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "op", "done": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"labels": f.runLabels})
	})
	mux.HandleFunc("/gcs/b/bk", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var b struct{ Labels map[string]string }
			_ = json.NewDecoder(r.Body).Decode(&b)
			for k, v := range b.Labels {
				f.bucket[k] = v
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"labels": f.bucket})
	})
	return mux
}

func client(t *testing.T, f *fakeGCP) *REST {
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return NewRESTWithClient(srv.Client()).WithBase("compute", srv.URL+"/compute").WithBase("run", srv.URL+"/run").WithBase("gcs", srv.URL+"/gcs")
}

func TestREST_ComputeDiskMergeKeepsOtherLabelsAndSendsFingerprint(t *testing.T) {
	f := &fakeGCP{diskLabels: map[string]string{"owner": "x", "product": "old"}, fp: "fp1"}
	c := client(t, f)
	after, err := c.Merge(context.Background(), Target{Project: "p", Type: TypeComputeDisk, Name: "d", Location: "z"}, map[string]string{"product": "new", "env": "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if f.gotFP != "fp1" {
		t.Errorf("fingerprint sent = %q", f.gotFP)
	}
	if after["owner"] != "x" || after["product"] != "new" || after["env"] != "prod" {
		t.Errorf("after = %v", after)
	}
}

func TestREST_CloudRunServiceUsesUpdateMask(t *testing.T) {
	f := &fakeGCP{runLabels: map[string]string{"a": "b"}}
	after, err := client(t, f).Merge(context.Background(), Target{Project: "p", Type: TypeCloudRunService, Name: "s", Location: "r"}, map[string]string{"product": "x"})
	if err != nil || after["a"] != "b" || after["product"] != "x" {
		t.Fatalf("%v %v", after, err)
	}
}

func TestREST_ActAsDeniedIsExplained(t *testing.T) {
	f := &fakeGCP{runStatus: 403, runBody: `{"error":{"code":403,"message":"Permission 'iam.serviceaccounts.actAs' denied on service account x"}}`}
	_, err := client(t, f).Merge(context.Background(), Target{Project: "p", Type: TypeCloudRunService, Name: "s", Location: "r"}, map[string]string{"product": "x"})
	var le *Error
	if !errors.As(err, &le) || le.Code != CodePermission || !strings.Contains(le.Hint, "act-as") {
		t.Fatalf("err = %v", err)
	}
}

func TestREST_NotFoundAndInvalid(t *testing.T) {
	c := client(t, &fakeGCP{})
	_, err := c.Get(context.Background(), Target{Project: "p", Type: TypeComputeDisk, Name: "nope", Location: "z"})
	var le *Error
	if !errors.As(err, &le) || le.Code != CodeNotFound {
		t.Fatalf("not found: %v", err)
	}
	if _, err = c.Merge(context.Background(), Target{Project: "p", Type: TypeGCSBucket, Name: "bk"}, map[string]string{"Bad Key": "x"}); !errors.As(err, &le) || le.Code != CodeInvalid {
		t.Fatalf("invalid: %v", err)
	}
	if _, err = c.Get(context.Background(), Target{Project: "p", Type: TypeComputeDisk, Name: "d"}); !errors.As(err, &le) || le.Code != CodeInvalid {
		t.Fatalf("missing zone: %v", err)
	}
}

func TestPlanDiffAndTerraform(t *testing.T) {
	d := PlanDiff(map[string]string{"product": "a", "env": "prod"}, map[string]string{"product": "b", "env": "prod", "team": "x"})
	got := map[string]string{}
	for _, x := range d {
		got[x.Key] = x.Change
	}
	if got["product"] != "change" || got["env"] != "same" || got["team"] != "add" {
		t.Errorf("diff = %v", got)
	}
	tf := Terraform(Target{Type: TypeComputeInstance, Name: "vm"}, map[string]string{"product": "web", "env": "prod"})
	if !strings.Contains(tf, "google_compute_instance") || !strings.Contains(tf, `env = "prod"`) || !strings.Contains(tf, `product = "web"`) {
		t.Errorf("tf = %s", tf)
	}
	sql := Terraform(Target{Type: TypeCloudSQLInstance, Name: "db"}, map[string]string{"product": "web"})
	if !strings.Contains(sql, "settings {") || !strings.Contains(sql, "user_labels") {
		t.Errorf("sql tf = %s", sql)
	}
	if !ValidLabel("product", "a-b_9") || ValidLabel("Product", "x") || ValidLabel("product", "A") || ValidLabel("1a", "x") {
		t.Error("ValidLabel")
	}
}
