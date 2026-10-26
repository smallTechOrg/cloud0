package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/smallTechOrg/cloud0/internal/errorsrc"
)

type sampleIter struct {
	entries []errorsrc.RawEntry
	i       int
}

func (s *sampleIter) Next() (errorsrc.RawEntry, error) {
	if s.i >= len(s.entries) {
		return errorsrc.RawEntry{}, iterator.Done
	}
	s.i++
	return s.entries[s.i-1], nil
}

type fakeSamples struct {
	entries []errorsrc.RawEntry
	err     error
	calls   int
	project string
	filter  string
}

func (f *fakeSamples) Entries(_ context.Context, project, filter string) (errorsrc.EntryIterator, error) {
	f.calls++
	f.project, f.filter = project, filter
	if f.err != nil {
		return nil, f.err
	}
	return &sampleIter{entries: f.entries}, nil
}

func samplesFixture(t *testing.T, fs *fakeSamples) *fixture {
	t.Helper()
	f := p2Fixture(t)
	f.deps.Samples = fs
	f.srv.Close()
	f.srv = newServerFor(t, f.deps)
	return f
}

func sEntry(status int, u, ip string) errorsrc.RawEntry {
	return errorsrc.RawEntry{Timestamp: phase1Now, Status: status, Method: "GET", URL: u, UserAgent: "ua", RemoteIP: ip, Latency: 5 * time.Millisecond, Trace: "t"}
}

func TestErrorSamples_AggregatesAndHidesFullIPs(t *testing.T) {
	fs := &fakeSamples{entries: []errorsrc.RawEntry{
		sEntry(404, "https://h/.env?a=b", "203.0.113.50"),
		sEntry(404, "https://h/.env", "203.0.113.51"),
		sEntry(502, "https://h/api", "198.51.100.7"),
	}}
	f := samplesFixture(t, fs)
	res, body, raw := f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W42&service=svc-a&project=project-a&limit=2", "")
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, raw)
	}
	if body["sampled"] != true || num(body["scanned"]) != 3 || body["service"] != "svc-a" || body["retention_note"] == nil {
		t.Fatalf("body = %v", body)
	}
	tp := asList(t, body["top_paths"])
	first := asMap(t, tp[0])
	if first["path"] != ".env" && first["path"] != "/.env" || first["scanner_like"] != true || num(first["count"]) != 2 {
		t.Fatalf("top path = %v", first)
	}
	if len(asList(t, body["entries"])) != 2 || num(asMap(t, body["status_counts"])["404"]) != 2 {
		t.Fatalf("entries/status_counts: %v", body)
	}
	if strings.Contains(string(raw), "203.0.113.50") || strings.Contains(string(raw), "198.51.100.7") || !strings.Contains(string(raw), "203.0.113.0/24") {
		t.Fatalf("full IP leaked or not truncated: %s", raw)
	}
	if lu, _ := body["logs_url"].(string); !strings.Contains(lu, "console.cloud.google.com/logs/query") {
		t.Fatalf("logs_url = %v", body["logs_url"])
	}
	if fs.project != "project-a" || !strings.Contains(fs.filter, `service_name="svc-a"`) || !strings.Contains(fs.filter, "2026-10-12T00:00:00Z") {
		t.Fatalf("project %q filter %q", fs.project, fs.filter)
	}
}

func TestErrorSamples_OutOfRetentionIsOK(t *testing.T) {
	fs := &fakeSamples{}
	f := samplesFixture(t, fs)
	res, body, raw := f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W35&service=svc-a&project=project-a", "")
	if res.StatusCode != 200 || body["out_of_retention"] != true || num(body["scanned"]) != 0 || fs.calls != 0 || body["retention_note"] == "" {
		t.Fatalf("status %d body %s calls %d", res.StatusCode, raw, fs.calls)
	}
}

func TestErrorSamples_ValidationAndFailures(t *testing.T) {
	fs := &fakeSamples{}
	f := samplesFixture(t, fs)
	for _, c := range []struct{ q, code string }{
		{"?week=2026-W42&project=project-a", "invalid_request"},
		{"?week=2026-W42&service=s", "invalid_request"}, // two projects configured
		{"?week=2026-W42&service=s&project=zzz", "invalid_scope"},
		{"?week=bad&service=s&project=project-a", "invalid_week"},
		{"?week=2026-W42&service=s&project=project-a&limit=0", "invalid_request"},
		{"?week=2026-W42&service=s&project=project-a&limit=9999", "invalid_request"},
		{"?week=2026-W42&service=s&project=project-a&min_status=x", "invalid_request"},
	} {
		res, body, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/samples"+c.q, "")
		expectError(t, res, body, 400, c.code)
	}

	fs.err = status.Error(codes.PermissionDenied, "caller lacks permission")
	res, body, _ := f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W42&service=s&project=project-a", "")
	e := expectError(t, res, body, 403, "permission_denied")
	if h, _ := e["hint"].(string); !strings.Contains(h, "roles/logging.viewer") {
		t.Fatalf("hint = %v", e["hint"])
	}
	fs.err = errors.New("boom")
	res, body, _ = f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W42&service=s&project=project-a", "")
	e = expectError(t, res, body, 502, "samples_failed")
	if h, _ := e["hint"].(string); strings.Contains(h, "roles/logging.viewer") || strings.Contains(h, "grant") {
		t.Fatalf("non-permission error must not carry the role hint: %v", e["hint"])
	}
	res, body, _ = f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W42&service=s&project=project-a&focus=bogus", "")
	expectError(t, res, body, 400, "invalid_request")

	f.deps.Samples = nil
	f.srv.Close()
	f.srv = newServerFor(t, f.deps)
	res, body, _ = f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W42&service=s&project=project-a", "")
	expectError(t, res, body, 503, "samples_unavailable")
	res, body, _ = f.doJSON(t, http.MethodPost, "/api/v1/errors/samples", "")
	expectError(t, res, body, 405, "method_not_allowed")
}

func newServerFor(t *testing.T, deps Deps) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(NewHandler(deps))
	t.Cleanup(srv.Close)
	return srv
}

func TestErrorSamples_FocusAndMalformedURLs(t *testing.T) {
	fs := &fakeSamples{entries: []errorsrc.RawEntry{
		sEntry(502, "https://h/%u002f/.env", "198.51.100.7"),
		sEntry(502, "/a%zz?b=%", "198.51.100.7"),
	}}
	f := samplesFixture(t, fs)
	res, body, raw := f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W42&service=svc-a&project=project-a&focus=server", "")
	if res.StatusCode != 200 || body["focus"] != "server" || num(body["scanned"]) != 2 || num(body["unparsed"]) != 2 {
		t.Fatalf("status %d: %s", res.StatusCode, raw)
	}
	if !strings.Contains(fs.filter, "httpRequest.status>=500") || strings.Contains(fs.filter, ">=400") {
		t.Fatalf("filter %q", fs.filter)
	}
	if lu, _ := body["logs_url"].(string); !strings.Contains(lu, "status%3E%3D500") {
		t.Fatalf("logs_url %v", body["logs_url"])
	}
	_, body, _ = f.doJSON(t, http.MethodGet, "/api/v1/errors/samples?week=2026-W42&service=svc-a&project=project-a", "")
	if body["focus"] != "all" {
		t.Fatalf("default focus = %v", body["focus"])
	}
}
