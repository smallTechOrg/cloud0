package attribution

import "testing"

func TestValidName(t *testing.T) {
	good := []string{"a", "0", "product-a", "a_b", "9x", "abcdefghijabcdefghijabcdefghijabcdefghij"}
	bad := []string{"", "-a", "_a", "A", "a b", "a.b", "abcdefghijabcdefghijabcdefghijabcdefghijk", "é"}
	for _, s := range good {
		if !engValidName(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if engValidName(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestContainsFold(t *testing.T) {
	cases := []struct {
		s, sub string
		want   bool
	}{
		{"Hello World", "o w", true}, {"Hello", "", true}, {"abc", "abcd", false},
		{"ABC", "bc", true}, {"abc", "ac", false}, {"Straße-X", "x", true},
		{"ÄBC", "äbc", true}, {"ÄBC", "bc", true}, {"abc", "é", false},
	}
	for _, c := range cases {
		if got := engContainsFold(c.s, c.sub); got != c.want {
			t.Errorf("engContainsFold(%q,%q)=%v", c.s, c.sub, got)
		}
	}
}

func TestHasPrefixFold(t *testing.T) {
	if !engHasPrefixFold("PROD-x", "prod") || engHasPrefixFold("pro", "prod") || engHasPrefixFold("xprod", "prod") {
		t.Fatal("ascii prefix")
	}
	if !engHasPrefixFold("ÄBC", "äb") {
		t.Fatal("unicode prefix")
	}
}

func TestLastSegment(t *testing.T) {
	for in, want := range map[string]string{"a/b/c": "c", "c": "c", "a/": "", "": ""} {
		if got := engLastSegment(in); got != want {
			t.Errorf("engLastSegment(%q)=%q want %q", in, got, want)
		}
	}
}

func TestLabelValueDeterministic(t *testing.T) {
	m := map[string]string{"Env": "b", "ENV": "a", "other": "z"}
	for i := 0; i < 50; i++ {
		if v, ok := engLabelValue(m, "env"); !ok || v != "a" { // "ENV" < "Env"
			t.Fatalf("got %q %v", v, ok)
		}
	}
	m["env"] = "exact"
	if v, _ := engLabelValue(m, "env"); v != "exact" {
		t.Fatalf("exact key should win, got %q", v)
	}
	if _, ok := engLabelValue(nil, "env"); ok {
		t.Fatal("nil map")
	}
}
