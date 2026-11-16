package errorsrc

import (
	"context"
	"strings"
	"testing"
)

func TestParsePath_Lenient(t *testing.T) {
	long := "/" + strings.Repeat("a", 500)
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"bad unicode escape", "https://host.example/%u002f/.env", "/%u002f/.env", false},
		{"lone percent", "/%", "/%", false},
		{"bad escape with bad query", "/a%zz?b=%", "/a%zz", false},
		{"bad escape absolute", "https://host.example/a%zz?b=%", "/a%zz", false},
		{"host only", "https://host.example", "/", true},
		{"empty", "", "/", true},
		{"good escape decoded", "https://host.example/a%20b?x=1", "/a b", true},
		{"unicode", "https://host.example/café/日本?q=1", "/café/日本", true},
		{"unicode percent", "/caf%C3%A9", "/café", true},
		{"query only bad", "https://host.example?x=%", "/", true},
	}
	for _, c := range cases {
		got, ok := ParsePath(c.in)
		if got != c.want {
			t.Errorf("%s: ParsePath(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
		if c.name != "query only bad" && ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
		}
	}
	for _, in := range []string{long, "https://h.example" + long, "/%zz" + long, "/" + strings.Repeat("日", 200)} {
		got, _ := ParsePath(in)
		if len(got) > MaxPathLen || len(got) == 0 {
			t.Errorf("long path len %d", len(got))
		}
		if strings.ContainsRune(got, '�') {
			t.Errorf("truncation broke utf8: %q", got)
		}
	}
}

func TestSampleErrors_MalformedURLsDoNotFailRead(t *testing.T) {
	es := []RawEntry{
		spEntry(404, "https://host.example/%u002f/.env", "192.0.2.1"),
		spEntry(404, "/%", "192.0.2.1"),
		spEntry(404, "/a%zz?b=%", "192.0.2.1"),
		spEntry(404, "", "192.0.2.1"),
		spEntry(404, "https://host.example/ok", "192.0.2.1"),
		spEntry(500, "https://host.example/"+strings.Repeat("x", 1000), "192.0.2.1"),
	}
	res, err := SampleErrors(context.Background(), &fakeEntries{it: &fakeIter{entries: es}}, SampleParams{Window: crW42, Now: crNow})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 6 || res.Unparsed != 3 {
		t.Fatalf("scanned=%d unparsed=%d", res.Scanned, res.Unparsed)
	}
	for _, e := range res.Entries {
		if len(e.Path) > MaxPathLen {
			t.Fatalf("path not truncated: %d", len(e.Path))
		}
	}
	if res.Entries[0].Path != "/%u002f/.env" || !res.Entries[0].ScannerLike {
		t.Fatalf("entry0 %+v", res.Entries[0])
	}
}

func TestFocusFilters(t *testing.T) {
	for focus, want := range map[string]string{
		FocusServer:   "httpRequest.status>=500",
		FocusNotFound: "httpRequest.status=404",
		FocusClient:   "(httpRequest.status=400 OR httpRequest.status=401 OR httpRequest.status=403 OR httpRequest.status=405 OR httpRequest.status=422 OR httpRequest.status=429)",
		FocusAll:      "httpRequest.status>=400",
		"":            "httpRequest.status>=400",
	} {
		src := &fakeEntries{it: &fakeIter{}}
		res, err := SampleErrors(context.Background(), src, SampleParams{Service: "svc", Window: crW42, Now: crNow, Focus: focus})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(src.filter, want) || (focus == FocusServer && strings.Contains(src.filter, ">=400")) {
			t.Errorf("focus %q: filter %q lacks %q", focus, src.filter, want)
		}
		if focus == "" && res.Focus != FocusAll || focus != "" && res.Focus != focus {
			t.Errorf("focus echo %q for %q", res.Focus, focus)
		}
	}
	if !ValidFocus("") || !ValidFocus("server") || ValidFocus("bogus") {
		t.Fatal("ValidFocus")
	}
	if u := FocusLogsExplorerURL("p", "svc", FocusServer, 400, crW42); !strings.Contains(u, "status%3E%3D500") {
		t.Fatal(u)
	}
}
