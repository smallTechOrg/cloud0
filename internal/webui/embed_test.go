package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, method, target string) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	res := rec.Result()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func TestHandler_RootServesHTMLWithUIMarkerAndNoCache(t *testing.T) {
	h := Handler()
	res, body := get(t, h, http.MethodGet, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("content-type %q", res.Header.Get("Content-Type"))
	}
	if res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("cache-control %q", res.Header.Get("Cache-Control"))
	}
	want := `data-cloud0-ui="` + string(CurrentMode()) + `"`
	if !strings.Contains(body, want) {
		t.Fatalf("body lacks %s", want)
	}
	if IsReal() != (CurrentMode() == ModeReal) {
		t.Fatal("IsReal and CurrentMode disagree")
	}
}

func TestHandler_UnknownPathFallsBackToIndexWith200(t *testing.T) {
	res, body := get(t, Handler(), http.MethodGet, "/some/client/route?x=1")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d", res.StatusCode)
	}
	if !strings.Contains(body, "data-cloud0-ui=") {
		t.Fatal("fallback did not serve the index page")
	}
}

func TestHandler_RejectsNonGetMethods(t *testing.T) {
	res, _ := get(t, Handler(), http.MethodPost, "/")
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", res.StatusCode)
	}
	res, _ = get(t, Handler(), http.MethodHead, "/")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status %d, want 200", res.StatusCode)
	}
}

func TestHandler_PlaceholderPageTellsUserToBuild(t *testing.T) {
	if IsReal() {
		t.Skip("real UI embedded; placeholder page content covered by its static file")
	}
	_, body := get(t, Handler(), http.MethodGet, "/")
	if !strings.Contains(body, "make web") || !strings.Contains(body, `data-cloud0-ui="placeholder"`) {
		t.Fatal("placeholder page lacks build instructions or marker")
	}
}

func TestHandler_RealBuildServesStaticAssetsImmutable(t *testing.T) {
	if !IsReal() {
		t.Skip("no frontend build embedded (run make web-build); asset serving is asserted by tests/scaffold when built")
	}
	_, index := get(t, Handler(), http.MethodGet, "/")
	// Find one linked stylesheet or script and fetch it.
	i := strings.Index(index, `/_next/static/`)
	if i < 0 {
		t.Fatal("index.html does not reference /_next/static/")
	}
	end := strings.IndexAny(index[i:], `"'`)
	asset := index[i : i+end]
	res, body := get(t, Handler(), http.MethodGet, asset)
	if res.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("asset %s: status %d, %d bytes", asset, res.StatusCode, len(body))
	}
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("asset cache-control %q", cc)
	}
}
