package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/store"
)

func TestAnnotations_PutGetUpdateAndEmptyPutDeletes(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)
	const path = "/api/v1/annotations/2026-W42"

	res, m, _ := f.doJSON(t, http.MethodGet, path, "")
	expectError(t, res, m, 404, "not_found")

	res, m, raw := f.doJSON(t, http.MethodPut, path, `{"body":"LB added for staging"}`)
	if res.StatusCode != 200 || m["week"] != "2026-W42" || m["body"] != "LB added for staging" || m["created_at"] != "2026-10-26T09:00:00Z" || m["updated_at"] != "2026-10-26T09:00:00Z" {
		t.Fatalf("put = %d %s", res.StatusCode, raw)
	}
	if len(m) != 4 {
		t.Fatalf("annotation body must have exactly 4 fields: %s", raw)
	}
	res, m, _ = f.doJSON(t, http.MethodGet, path, "")
	if res.StatusCode != 200 || m["body"] != "LB added for staging" {
		t.Fatalf("get = %d %v", res.StatusCode, m)
	}

	// Second save: body and updated_at change, created_at is kept (multi-interaction).
	now = now.Add(2 * time.Minute)
	res, m, _ = f.doJSON(t, http.MethodPut, path, `{"body":"LB added for staging — reverted Friday"}`)
	if res.StatusCode != 200 || m["created_at"] != "2026-10-26T09:00:00Z" || m["updated_at"] != "2026-10-26T09:02:00Z" || !strings.Contains(m["body"].(string), "reverted") {
		t.Fatalf("second put = %d %v", res.StatusCode, m)
	}
	// The table row carries it; a not-fetched week may have a note.
	_, tbl, _ := f.doJSON(t, http.MethodGet, "/api/v1/cost/table", "")
	if rowFor(t, tbl, "2026-W42")["annotation"] != m["body"] || rowFor(t, tbl, "2026-W41")["annotation"] != nil {
		t.Fatal("table annotation column wrong")
	}

	// Empty (after trim) body deletes.
	res, m, raw = f.doJSON(t, http.MethodPut, path, `{"body":"   \n"}`)
	if res.StatusCode != 200 || m["week"] != "2026-W42" || m["body"] != "" || m["created_at"] != nil || m["updated_at"] != nil {
		t.Fatalf("empty put = %d %s", res.StatusCode, raw)
	}
	res, m, _ = f.doJSON(t, http.MethodGet, path, "")
	expectError(t, res, m, 404, "not_found")
	// Deleting again is idempotent.
	if res, _, _ = f.doJSON(t, http.MethodPut, path, `{"body":""}`); res.StatusCode != 200 {
		t.Fatalf("second empty put = %d", res.StatusCode)
	}
}

func TestAnnotations_Validation(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)

	res, m, _ := f.doJSON(t, http.MethodGet, "/api/v1/annotations/2026-W60", "")
	expectError(t, res, m, 400, "invalid_week")
	res, m, _ = f.doJSON(t, http.MethodPut, "/api/v1/annotations/garbage", `{"body":"x"}`)
	expectError(t, res, m, 400, "invalid_week")
	res, m, _ = f.doJSON(t, http.MethodPut, "/api/v1/annotations/2026-W42", `{"body":"`+strings.Repeat("é", 10001)+`"}`)
	e := expectError(t, res, m, 400, "annotation_too_long")
	if e["hint"] == nil {
		t.Fatal("annotation_too_long should carry a hint")
	}
	if res, _, _ = f.doJSON(t, http.MethodPut, "/api/v1/annotations/2026-W42", `{"body":"`+strings.Repeat("é", 10000)+`"}`); res.StatusCode != 200 {
		t.Fatalf("exactly 10,000 characters must be accepted, got %d", res.StatusCode)
	}
	for _, body := range []string{`{}`, `{"body":null}`, `{"body":5}`, `{"body":"x","week":"2026-W42"}`, `{"body":`, ``} {
		res, m, _ = f.doJSON(t, http.MethodPut, "/api/v1/annotations/2026-W43", body)
		expectError(t, res, m, 400, "invalid_request")
	}
	res, m, _ = f.doJSON(t, http.MethodDelete, "/api/v1/annotations/2026-W42", "")
	expectError(t, res, m, 405, "method_not_allowed")
}

func TestAnnotations_SurviveStoreReopen(t *testing.T) {
	now := phase1Now
	f := newPhase1Fixture(t, &fakeSource{}, &now)
	if res, _, _ := f.doJSON(t, http.MethodPut, "/api/v1/annotations/2026-W40", `{"body":"persisted"}`); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	path := f.store.Path()
	f.srv.Close()
	_ = f.store.Close()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f2 := newFixtureOn(t, st, testConfig(t), &fakeSource{}, &now)
	res, m, _ := f2.doJSON(t, http.MethodGet, "/api/v1/annotations/2026-W40", "")
	if res.StatusCode != 200 || m["body"] != "persisted" {
		t.Fatalf("after reopen = %d %v", res.StatusCode, m)
	}
}
