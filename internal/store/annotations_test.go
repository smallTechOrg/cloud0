package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/smallTechOrg/cloud0/internal/isoweek"
)

func TestAnnotations_UpsertGetUpdateDeleteLifecycle(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	w := isoweek.MustParse("2026-W42")

	if _, err := GetAnnotation(ctx, s.DB, w); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing note err = %v, want ErrNotFound", err)
	}

	first, err := UpsertAnnotation(ctx, s.DB, w, "LB added for staging", testNow)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if first.Week != "2026-W42" || first.Body != "LB added for staging" || first.CreatedAt == nil || first.UpdatedAt == nil || !first.CreatedAt.Equal(testNow) || !first.UpdatedAt.Equal(testNow) {
		t.Fatalf("first = %+v", first)
	}
	got, err := GetAnnotation(ctx, s.DB, w)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != first.Body || !got.CreatedAt.Equal(*first.CreatedAt) {
		t.Fatalf("get = %+v", got)
	}

	// Second save on the same week updates body and updated_at, keeps created_at.
	later := testNow.Add(2 * time.Hour)
	second, err := UpsertAnnotation(ctx, s.DB, w, "LB added for staging; moved DB", later)
	if err != nil {
		t.Fatal(err)
	}
	if second.Body != "LB added for staging; moved DB" || !second.CreatedAt.Equal(testNow) || !second.UpdatedAt.Equal(later) {
		t.Fatalf("second = %+v", second)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM annotations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("annotations rows = %d, want 1 (upsert, not insert)", n)
	}

	// Empty-after-trim body deletes and reports the deleted shape.
	deleted, err := UpsertAnnotation(ctx, s.DB, w, "  \n\t ", later)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Week != "2026-W42" || deleted.Body != "" || deleted.CreatedAt != nil || deleted.UpdatedAt != nil {
		t.Fatalf("deleted = %+v", deleted)
	}
	if _, err := GetAnnotation(ctx, s.DB, w); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete err = %v", err)
	}
	// Deleting again is a no-op, not an error.
	if err := DeleteAnnotation(ctx, s.DB, w); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestAnnotations_RejectsTooLongAndKeepsBodyVerbatim(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	w := isoweek.MustParse("2026-W43")

	exact := strings.Repeat("ß", MaxAnnotationLen) // counted in characters, not bytes
	if _, err := UpsertAnnotation(ctx, s.DB, w, exact, testNow); err != nil {
		t.Fatalf("%d-char note rejected: %v", MaxAnnotationLen, err)
	}
	if _, err := UpsertAnnotation(ctx, s.DB, w, exact+"x", testNow); !errors.Is(err, ErrAnnotationTooLong) {
		t.Fatalf("%d-char note err = %v, want ErrAnnotationTooLong", MaxAnnotationLen+1, err)
	}
	got, _ := GetAnnotation(ctx, s.DB, w)
	if got.Body != exact {
		t.Fatal("rejected write changed the stored body")
	}
	// Surrounding whitespace is preserved when the note is non-empty.
	saved, err := UpsertAnnotation(ctx, s.DB, w, "  note  ", testNow)
	if err != nil || saved.Body != "  note  " {
		t.Fatalf("body not stored verbatim: %+v (%v)", saved, err)
	}
	if _, err := UpsertAnnotation(ctx, s.DB, isoweek.Week{Year: 2027, Week: 53}, "x", testNow); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("invalid week err = %v", err)
	}
}

func TestAnnotationsInRange_ReturnsBodiesByWeekAndSurvivesReopen(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	w41, w42, w44 := isoweek.MustParse("2026-W41"), isoweek.MustParse("2026-W42"), isoweek.MustParse("2026-W44")
	for w, body := range map[isoweek.Week]string{w41: "a", w42: "b", w44: "d", isoweek.MustParse("2027-W01"): "next year"} {
		if _, err := UpsertAnnotation(ctx, s.DB, w, body, testNow); err != nil {
			t.Fatal(err)
		}
	}
	got, err := AnnotationsInRange(ctx, s.DB, w42, isoweek.MustParse("2026-W53"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[w42] != "b" || got[w44] != "d" {
		t.Fatalf("range = %v", got)
	}
	if _, err := AnnotationsInRange(ctx, s.DB, w44, w41); err == nil {
		t.Error("reversed range accepted")
	}

	path := s.Path()
	_ = s.Close()
	re, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	again, err := GetAnnotation(ctx, re.DB, w42)
	if err != nil || again.Body != "b" {
		t.Fatalf("after reopen = %+v (%v)", again, err)
	}
}

func TestAnnotation_JSONShapeMatchesAPI(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	w := isoweek.MustParse("2026-W42")
	saved, _ := UpsertAnnotation(ctx, s.DB, w, "note", testNow)
	b, _ := json.Marshal(saved)
	want := `{"week":"2026-W42","body":"note","created_at":"2026-10-26T09:00:00Z","updated_at":"2026-10-26T09:00:00Z"}`
	if string(b) != want {
		t.Fatalf("json = %s\nwant   %s", b, want)
	}
	deleted, _ := UpsertAnnotation(ctx, s.DB, w, "", testNow)
	b, _ = json.Marshal(deleted)
	want = `{"week":"2026-W42","body":"","created_at":null,"updated_at":null}`
	if string(b) != want {
		t.Fatalf("deleted json = %s\nwant   %s", b, want)
	}
}
