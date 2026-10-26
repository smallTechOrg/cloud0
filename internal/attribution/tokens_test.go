package attribution

import (
	"reflect"
	"sort"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"vm-product-a-prod", []string{"vm", "product", "a", "prod"}},
		{"Svc_X.web/2", []string{"svc", "x", "web"}},
		{"vm2web", []string{"vm", "web"}},
		{"projects/p1/zones/us-central1-a", []string{"projects", "p", "zones", "us", "central", "a"}},
		{"123-456", nil},
		{"", nil},
	}
	for _, c := range cases {
		if got := Tokenize(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokenize(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestStopTokens(t *testing.T) {
	list := StopTokens()
	if !sort.StringsAreSorted(list) || len(list) < 50 {
		t.Fatalf("stop-list must be sorted and substantial, got %d", len(list))
	}
	for _, w := range []string{"vm", "instance", "instances", "projects", "backend", "frontend", "storefront", "web", "api", "db",
		"bucket", "media", "backups", "cache", "migrate", "jobs", "repo", "ip", "lb", "default", "prod", "staging", "dev", "beta", "test", "us", "central1"} {
		if w == "central1" {
			w = "central" // digits are stripped by Tokenize
		}
		if !IsStopToken(w) {
			t.Errorf("%q must be a stop token", w)
		}
	}
	if IsStopToken("product") {
		t.Error("product is not generic")
	}
	if !IsStopToken("WEB") {
		t.Error("IsStopToken must be case-insensitive")
	}
}

func TestEnvForToken(t *testing.T) {
	for tok, want := range map[string]string{"prod": "prod", "Production": "prod", "stage": "staging", "staging": "staging",
		"dev": "dev", "development": "dev", "beta": "beta", "test": "test", "qa": "qa"} {
		if got, ok := EnvForToken(tok); !ok || got != want {
			t.Errorf("EnvForToken(%q) = %q,%v want %q", tok, got, ok, want)
		}
	}
	if _, ok := EnvForToken("web"); ok {
		t.Error("web is not an environment")
	}
}
