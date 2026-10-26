package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestChangeHistory_InsertListFilterLimit(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	a, err := InsertChange(ctx, s.DB, NewChange{At: testNow, Kind: "policy", Action: "create", Target: "projects/proj-a/alertPolicies/1",
		After: json.RawMessage(`{"displayName":"x"}`), PreviewHash: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Actor != "local-user" || string(a.Before) != "null" {
		t.Fatalf("a = %+v", a)
	}
	for i, k := range []string{"budget", "policy", "budget"} {
		if _, err := InsertChange(ctx, s.DB, NewChange{At: testNow.Add(time.Duration(i+1) * time.Minute), Kind: k, Action: "update", Target: "t" + k,
			Before: json.RawMessage(`{"a":1}`), After: json.RawMessage(`{"a":2}`)}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := ListChanges(ctx, s.DB, ChangeFilter{}, 0)
	if err != nil || len(all) != 4 || all[0].Kind != "budget" || all[3].ID != a.ID {
		t.Fatalf("all = %+v err=%v", all, err)
	}
	if string(all[3].After) != `{"displayName":"x"}` || string(all[3].Before) != "null" || !all[3].At.Equal(testNow) {
		t.Fatalf("roundtrip = %+v", all[3])
	}
	b, _ := ListChanges(ctx, s.DB, ChangeFilter{Kind: "budget"}, 1)
	if len(b) != 1 || b[0].Kind != "budget" {
		t.Fatalf("budget limit 1 = %+v", b)
	}
	none, err := ListChanges(ctx, s.DB, ChangeFilter{Kind: "snooze"}, 10)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("none = %#v err=%v", none, err)
	}
	byT, _ := ListChanges(ctx, s.DB, ChangeFilter{Target: "projects/proj-a/alertPolicies/1"}, 10)
	if len(byT) != 1 {
		t.Fatalf("by target = %+v", byT)
	}
}

func TestChangeHistory_RedactsAndValidates(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	e, err := InsertChange(ctx, s.DB, NewChange{At: testNow, Kind: "channel", Action: "create", Target: "c",
		After: json.RawMessage(`{"labels":{"auth_token":"SECRETVALUE","channel_name":"#ops"},"list":[{"webhook_url":"https://x"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := ListChanges(ctx, s.DB, ChangeFilter{}, 5)
	if strings.Contains(string(rows[0].After), "SECRETVALUE") || strings.Contains(string(e.After), "https://x") || !strings.Contains(string(rows[0].After), "#ops") {
		t.Fatalf("not redacted: %s", rows[0].After)
	}
	if _, err := InsertChange(ctx, s.DB, NewChange{At: testNow, Kind: "", Action: "x", Target: "y"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v", err)
	}
	if _, err := InsertChange(ctx, s.DB, NewChange{Kind: "a", Action: "x", Target: "y"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("zero time err = %v", err)
	}
}

func TestChangeHistory_SurvivesReopen(t *testing.T) {
	s := openMigrated(t)
	ctx := context.Background()
	if _, err := InsertChange(ctx, s.DB, NewChange{At: testNow, Kind: "routing", Action: "update", Target: "alerts.routing"}); err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rows, err := ListChanges(ctx, s2.DB, ChangeFilter{}, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
