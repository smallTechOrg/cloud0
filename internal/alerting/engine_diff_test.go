package alerting

import (
	"strings"
	"testing"
)

func TestDiffAddRemoveChange(t *testing.T) {
	before := []byte(`{"name":"projects/p/alertPolicies/1","etag":"a","displayName":"x","enabled":true,"gone":1,
	  "conditions":[{"name":"c1","conditionThreshold":{"thresholdValue":0.8,"duration":"300s"}}]}`)
	after := []byte(`{"displayName":"y","enabled":true,"new":{"k":1},
	  "conditions":[{"conditionThreshold":{"thresholdValue":0.9,"duration":"300s"}},{"displayName":"second"}]}`)
	ch, err := DiffJSON(before, after)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DiffChange{}
	for _, c := range ch {
		got[c.Path] = c
	}
	want := map[string]string{
		"displayName": "change", "gone": "remove", "new": "add",
		"conditions[0].conditionThreshold.thresholdValue": "change",
		"conditions[1]": "add",
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %+v", ch)
	}
	for p, op := range want {
		if got[p].Op != op {
			t.Errorf("%s: op %q want %q", p, got[p].Op, op)
		}
	}
	if string(got["conditions[0].conditionThreshold.thresholdValue"].Before) != "0.8" || string(got["conditions[0].conditionThreshold.thresholdValue"].After) != "0.9" {
		t.Errorf("before/after wrong: %+v", got["conditions[0].conditionThreshold.thresholdValue"])
	}
}

func TestDiffArrayRemoveAndIgnored(t *testing.T) {
	ch, err := DiffJSON([]byte(`{"a":[1,2,3],"etag":"1","creationRecord":{"x":1}}`), []byte(`{"a":[1,2],"etag":"2","mutationRecord":{"y":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ch) != 1 || ch[0].Path != "a[2]" || ch[0].Op != "remove" {
		t.Fatalf("changes = %+v", ch)
	}
	same, _ := DiffJSON([]byte(`{"a":1}`), []byte(`{"a":1,"etag":"z"}`))
	if len(same) != 0 {
		t.Fatalf("identical docs differ: %+v", same)
	}
}

func TestDiffCreateAndDelete(t *testing.T) {
	ch, _ := DiffJSON(nil, []byte(`{"displayName":"x"}`))
	if len(ch) != 1 || ch[0].Op != "add" || ch[0].Path != "displayName" {
		t.Fatalf("create diff = %+v", ch)
	}
	ch, _ = DiffJSON([]byte(`{"displayName":"x"}`), nil)
	if len(ch) != 1 || ch[0].Op != "remove" {
		t.Fatalf("delete diff = %+v", ch)
	}
	if _, err := DiffJSON([]byte(`{bad`), nil); err == nil {
		t.Fatal("invalid JSON must error")
	}
}

func TestUnifiedDiff(t *testing.T) {
	u := engUnified([]byte(`{"a":1,"b":2,"c":3}`), []byte(`{"a":1,"b":5,"c":3}`), nil)
	for _, s := range []string{"--- current", "+++ desired", "- ", "+ ", `"b": 5`, `"b": 2`, "@@"} {
		if !strings.Contains(u, s) {
			t.Errorf("unified missing %q:\n%s", s, u)
		}
	}
	if engUnified([]byte(`{"a":1}`), []byte(`{"a":1}`), nil) != "" {
		t.Error("identical docs must give empty unified diff")
	}
}
