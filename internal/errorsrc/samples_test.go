package errorsrc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/iterator"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

type fakeIter struct {
	entries []RawEntry
	i       int
	err     error // returned instead of Done after entries
}

func (f *fakeIter) Next() (RawEntry, error) {
	if f.i >= len(f.entries) {
		if f.err != nil {
			return RawEntry{}, f.err
		}
		return RawEntry{}, iterator.Done
	}
	e := f.entries[f.i]
	f.i++
	return e, nil
}

type fakeEntries struct {
	it      *fakeIter
	openErr error
	project string
	filter  string
	calls   int
}

func (f *fakeEntries) Entries(_ context.Context, project, filter string) (EntryIterator, error) {
	f.calls++
	f.project, f.filter = project, filter
	if f.openErr != nil {
		return nil, f.openErr
	}
	return f.it, nil
}

var spNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func spEntry(status int, rawURL, ip string) RawEntry {
	return RawEntry{Timestamp: spNow, Status: status, Method: "GET", URL: rawURL, UserAgent: "ua", RemoteIP: ip, Latency: 12 * time.Millisecond, Trace: "projects/p/traces/abc"}
}

func TestSampleErrors_Aggregates(t *testing.T) {
	es := []RawEntry{
		spEntry(404, "https://svc.example/.git/config?x=1", "203.0.113.77"),
		spEntry(404, "https://svc.example/.git/config", "203.0.113.78"),
		spEntry(404, "https://svc.example/wp-login.php", "2001:db8:1234:5678::1"),
		spEntry(401, "https://svc.example/api/me?token=secret", "198.51.100.9"),
		spEntry(502, "https://svc.example/api/me", "198.51.100.9"),
	}
	src := &fakeEntries{it: &fakeIter{entries: es}}
	res, err := SampleErrors(context.Background(), src, SampleParams{Project: "p", Service: "svc", Window: crW42, Limit: 3, Now: crNow})
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 5 || !res.Sampled || res.OutOfRetention {
		t.Fatalf("scanned=%d sampled=%v oor=%v", res.Scanned, res.Sampled, res.OutOfRetention)
	}
	if res.StatusCounts["404"] != 3 || res.StatusCounts["401"] != 1 || res.StatusCounts["502"] != 1 {
		t.Fatalf("status counts %v", res.StatusCounts)
	}
	top := res.TopPaths[0]
	if top.Path != "/.git/config" || top.Status != 404 || top.Count != 2 || !top.ScannerLike {
		t.Fatalf("top = %+v", top)
	}
	for _, tp := range res.TopPaths {
		if strings.Contains(tp.Path, "?") || strings.Contains(tp.Path, "secret") {
			t.Fatalf("query string leaked: %+v", tp)
		}
		if tp.Path == "/api/me" && tp.ScannerLike {
			t.Fatalf("/api/me flagged as scanner")
		}
	}
	if len(res.Entries) != 3 {
		t.Fatalf("entries = %d, want limit 3", len(res.Entries))
	}
	if res.Entries[0].RemoteIP != "203.0.113.0/24" || res.Entries[2].RemoteIP != "2001:db8:1234::/48" {
		t.Fatalf("ips: %q %q", res.Entries[0].RemoteIP, res.Entries[2].RemoteIP)
	}
	if res.Entries[0].LatencyMS != 12 || res.Entries[0].Trace == "" || res.Entries[0].Path != "/.git/config" {
		t.Fatalf("entry fields: %+v", res.Entries[0])
	}
	// 4xx: three scanner-like (.git x2, wp-login.php) of four.
	if res.ScannerShare4xx == nil || *res.ScannerShare4xx != 0.75 {
		t.Fatalf("scanner share = %v", res.ScannerShare4xx)
	}
	if !strings.Contains(src.filter, `resource.labels.service_name="svc"`) || !strings.Contains(src.filter, "httpRequest.status>=400") ||
		!strings.Contains(src.filter, `timestamp>="2026-10-12T00:00:00Z"`) || src.project != "p" {
		t.Fatalf("filter %q project %q", src.filter, src.project)
	}
}

func TestSampleErrors_CapsScannedAt1000(t *testing.T) {
	var es []RawEntry
	for i := 0; i < 1500; i++ {
		es = append(es, spEntry(404, fmt.Sprintf("https://x/p%d", i%20), "192.0.2.1"))
	}
	res, err := SampleErrors(context.Background(), &fakeEntries{it: &fakeIter{entries: es}}, SampleParams{Window: crW42, Now: crNow})
	if err != nil || res.Scanned != MaxScanned {
		t.Fatalf("scanned=%d err=%v", res.Scanned, err)
	}
	if len(res.TopPaths) != MaxTopPaths || len(res.Entries) != DefaultSampleLimit {
		t.Fatalf("top=%d entries=%d", len(res.TopPaths), len(res.Entries))
	}
	if !strings.Contains(res.Note, "most recent entries") {
		t.Fatalf("note %q", res.Note)
	}
}

func TestSampleErrors_OutOfRetentionIsNotAnError(t *testing.T) {
	src := &fakeEntries{it: &fakeIter{}}
	old := gcpsource.Window{Start: time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)}
	res, err := SampleErrors(context.Background(), src, SampleParams{Window: old, Now: spNow})
	if err != nil || !res.OutOfRetention || res.RetentionNote == "" || res.Scanned != 0 || src.calls != 0 {
		t.Fatalf("res=%+v err=%v calls=%d", res, err, src.calls)
	}
}

func TestSampleErrors_Errors(t *testing.T) {
	boom := errors.New("rpc error: code = PermissionDenied desc = nope")
	if _, err := SampleErrors(context.Background(), &fakeEntries{openErr: boom}, SampleParams{Window: crW42, Now: crNow}); !errors.Is(err, boom) {
		t.Fatalf("open err = %v", err)
	}
	if _, err := SampleErrors(context.Background(), &fakeEntries{it: &fakeIter{err: boom}}, SampleParams{Window: crW42, Now: crNow}); !errors.Is(err, boom) {
		t.Fatalf("iter err = %v", err)
	}
	res, err := SampleErrors(context.Background(), &fakeEntries{it: &fakeIter{}}, SampleParams{Window: crW42, Now: crNow})
	if err != nil || res.Scanned != 0 || res.ScannerShare4xx != nil || len(res.Entries) != 0 {
		t.Fatalf("empty: %+v %v", res, err)
	}
}

func TestTruncateIP(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.77": "203.0.113.0/24", "2001:db8:1:2:3:4:5:6": "2001:db8:1::/48", "": "", "not-an-ip": "", "::ffff:198.51.100.5": "198.51.100.0/24",
	} {
		if got := TruncateIP(in); got != want {
			t.Errorf("TruncateIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripQuery(t *testing.T) {
	for in, want := range map[string]string{
		"https://h.example/a/b?x=1#f": "/a/b", "https://h.example": "/", "/plain?q=1": "/plain", "": "/", "https://h.example/.env": "/.env",
	} {
		if got := StripQuery(in); got != want {
			t.Errorf("StripQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikeScanner(t *testing.T) {
	yes := []string{"/.git/config", "/.env", "/config.php", "/wp-login.php", "/wp-admin/setup-config.php", "/wp-includes/x.js", "/backup.bak", "/db.sql",
		"/cgi-bin/luci", "/admin", "/admin/login", "/phpmyadmin/index.php", "/vendor/phpunit/x", "/a/.DS_Store", "/xmlrpc.php", "/actuator/health", "/.aws/credentials"}
	no := []string{"/", "/api/v1/users", "/static/app.js", "/login", "/.well-known/acme-challenge/x", "/favicon.ico", "/robots.txt"}
	for _, p := range yes {
		if !LooksLikeScanner(p) {
			t.Errorf("%q should look like a scanner", p)
		}
	}
	for _, p := range no {
		if LooksLikeScanner(p) {
			t.Errorf("%q should not look like a scanner", p)
		}
	}
	if len(ScannerPrefixes) == 0 || len(ScannerSuffixes) == 0 {
		t.Fatal("exported lists empty")
	}
}

func TestLogsExplorerURL(t *testing.T) {
	u := LogsExplorerURL("proj-x", "my-svc", crW42)
	if !strings.HasPrefix(u, "https://console.cloud.google.com/logs/query;query=") || !strings.HasSuffix(u, "?project=proj-x") {
		t.Fatal(u)
	}
	if !strings.Contains(u, ";timeRange=2026-10-12T00:00:00.000Z%2F2026-10-19T00:00:00.000Z?") {
		t.Fatal(u)
	}
	q := strings.TrimPrefix(u[:strings.Index(u, ";timeRange")], "https://console.cloud.google.com/logs/query;query=")
	dec, err := url.QueryUnescape(q)
	if err != nil || dec != RunRequestsFilter("my-svc", 400) || strings.ContainsAny(q, " \"") {
		t.Fatalf("query %q -> %q (%v)", q, dec, err)
	}
	if !strings.Contains(dec, `resource.type="cloud_run_revision"`) || !strings.Contains(dec, "httpRequest.status>=400") {
		t.Fatal(dec)
	}
}
