package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/labeler"
)

// fakeLabeler is an in-memory GCP: Get returns the stored labels, Merge writes them.
type fakeLabeler struct {
	mu     sync.Mutex
	labels map[string]map[string]string
	writes int
	failOn string
}

func (f *fakeLabeler) Get(_ context.Context, t labeler.Target) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.labels[t.Name]
	if !ok {
		return nil, &labeler.Error{Code: labeler.CodeNotFound, Message: "no such resource"}
	}
	out := map[string]string{}
	for k, v := range l {
		out[k] = v
	}
	return out, nil
}

func (f *fakeLabeler) Merge(_ context.Context, t labeler.Target, add map[string]string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t.Name == f.failOn {
		return nil, &labeler.Error{Code: labeler.CodePermission, Message: "denied"}
	}
	f.writes++
	f.labels[t.Name] = labeler.Merged(f.labels[t.Name], add)
	return f.labels[t.Name], nil
}

func labelsFx(t *testing.T, fl *fakeLabeler) *rtFx {
	f := rtNew(t, true)
	f.srv.Close()
	f.deps.Labeler = fl
	f.srv = httptest.NewServer(NewHandler(f.deps))
	t.Cleanup(f.srv.Close)
	return f
}

const twoItems = `{"items":[
 {"target":{"project":"project-a","type":"compute_disk","name":"disk-1","location":"zone-a"},"labels":{"product":"web","env":"prod"}},
 {"target":{"project":"project-a","type":"gcs_bucket","name":"bkt"},"labels":{"product":"web"}}]`

func TestLabels_503WhenNotConfigured(t *testing.T) {
	f := rtNew(t, true)
	for _, p := range []string{"/labels/preview", "/labels/apply"} {
		st, m := f.callJSON(t, "POST", "/api/v1"+p, twoItems+"}")
		if st != 503 || rtErrCode(m) != "labels_not_configured" {
			t.Errorf("%s: %d %v", p, st, m)
		}
	}
}

func (f *rtFx) callJSON(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return f.do(t, req)
}

func TestLabels_PreviewIsReadOnlyAndShowsDiff(t *testing.T) {
	fl := &fakeLabeler{labels: map[string]map[string]string{"disk-1": {"owner": "x"}, "bkt": {}}}
	f := labelsFx(t, fl)
	st, m := f.callJSON(t, "POST", "/api/v1/labels/preview", twoItems+"}")
	if st != 200 || fl.writes != 0 {
		t.Fatalf("preview: %d writes=%d %v", st, fl.writes, m)
	}
	items := m["items"].([]any)
	first := items[0].(map[string]any)
	if first["terraform"] == "" || first["command"] == nil {
		t.Errorf("missing terraform/command: %v", first)
	}
	if m["changes"].(float64) != 2 || m["preview_hash"] == "" {
		t.Errorf("changes/hash: %v", m)
	}
}

func TestLabels_ApplyWritesMergesRecordsHistoryAndDetectsStale(t *testing.T) {
	fl := &fakeLabeler{labels: map[string]map[string]string{"disk-1": {"owner": "x"}, "bkt": {}}}
	f := labelsFx(t, fl)
	_, pv := f.callJSON(t, "POST", "/api/v1/labels/preview", twoItems+"}")
	hash := pv["preview_hash"].(string)

	// Wrong hash is refused and writes nothing.
	st, m := f.callJSON(t, "POST", "/api/v1/labels/apply", twoItems+`,"preview_hash":"deadbeef"}`)
	if st != 409 || rtErrCode(m) != "preview_stale" || fl.writes != 0 {
		t.Fatalf("stale: %d %v writes=%d", st, m, fl.writes)
	}
	// Missing hash is a 400.
	if st, m = f.callJSON(t, "POST", "/api/v1/labels/apply", twoItems+"}"); st != 400 {
		t.Fatalf("no hash: %d %v", st, m)
	}
	// Correct hash writes, preserving unrelated labels.
	st, m = f.callJSON(t, "POST", "/api/v1/labels/apply", fmt.Sprintf(`%s,"preview_hash":%q}`, twoItems, hash))
	if st != 200 || m["applied"].(float64) != 2 || fl.writes != 2 {
		t.Fatalf("apply: %d %v writes=%d", st, m, fl.writes)
	}
	if got := fl.labels["disk-1"]; got["owner"] != "x" || got["product"] != "web" || got["env"] != "prod" {
		t.Errorf("merge lost labels: %v", got)
	}
	// History has one entry per written resource.
	_, h := f.callJSON(t, "GET", "/api/v1/history?kind=labels", "")
	if n := len(h["entries"].([]any)); n != 2 {
		t.Errorf("history entries = %d, want 2", n)
	}
	// Re-previewing the now-applied state is a no-op (nothing left to write).
	_, pv2 := f.callJSON(t, "POST", "/api/v1/labels/preview", twoItems+"}")
	if pv2["changes"].(float64) != 0 {
		t.Errorf("after apply changes = %v", pv2["changes"])
	}
}

func TestLabels_PartialFailureDoesNotAbortOthers(t *testing.T) {
	fl := &fakeLabeler{labels: map[string]map[string]string{"disk-1": {}, "bkt": {}}, failOn: "disk-1"}
	f := labelsFx(t, fl)
	_, pv := f.callJSON(t, "POST", "/api/v1/labels/preview", twoItems+"}")
	st, m := f.callJSON(t, "POST", "/api/v1/labels/apply", fmt.Sprintf(`%s,"preview_hash":%q}`, twoItems, pv["preview_hash"]))
	if st != 200 || m["applied"].(float64) != 1 || m["failed"].(float64) != 1 {
		t.Fatalf("partial: %d %v", st, m)
	}
}

func TestLabels_RejectsBadInput(t *testing.T) {
	f := labelsFx(t, &fakeLabeler{labels: map[string]map[string]string{}})
	for name, body := range map[string]string{
		"empty":        `{"items":[]}`,
		"bad label":    `{"items":[{"target":{"project":"p","type":"gcs_bucket","name":"b"},"labels":{"Product":"X Y"}}]}`,
		"no zone":      `{"items":[{"target":{"project":"p","type":"compute_disk","name":"d"},"labels":{"product":"x"}}]}`,
		"unknown type": `{"items":[{"target":{"project":"p","type":"vpn","name":"d"},"labels":{"product":"x"}}]}`,
	} {
		if st, m := f.callJSON(t, "POST", "/api/v1/labels/preview", body); st != 400 {
			t.Errorf("%s: %d %v", name, st, m)
		}
	}
}

func TestAttrResources_GroupsAndExport(t *testing.T) {
	f := rtNew(t, true)
	f.seedRows(t, "2026-W42")
	st, m := f.call(t, "GET", "/api/v1/attribution/resources?week=2026-W42", "")
	if st != 200 {
		t.Fatalf("%d %v", st, m)
	}
	groups := m["groups"].([]any)
	if len(groups) == 0 || m["label_writer"].(map[string]any)["enabled"] != false {
		t.Fatalf("groups/writer: %v", m)
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.(map[string]any)["name"].(string))
	}
	t.Logf("groups: %v", names)
	req, _ := http.NewRequest("GET", f.srv.URL+"/api/v1/attribution/export?week=2026-W42&format=csv", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("export: %v %v", err, res)
	}
	res.Body.Close()
}

func (f *rtFx) do(t *testing.T, req *http.Request) (int, map[string]any) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not JSON: %q", raw)
	}
	return res.StatusCode, m
}
