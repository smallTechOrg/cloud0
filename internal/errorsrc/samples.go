package errorsrc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/logging/logadmin"
	"google.golang.org/api/iterator"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// Sampling limits and notes (documented in spec/api.md → GET /errors/samples).
const (
	// MaxScanned caps how many log entries one sample call reads.
	MaxScanned = 1000
	// MaxTopPaths is the number of (path, status) rows returned in top_paths.
	MaxTopPaths = 10
	// DefaultSampleLimit / MaxSampleLimit bound the `entries` list.
	DefaultSampleLimit = 50
	MaxSampleLimit     = 200
	// RetentionDays is the Cloud Logging _Default bucket retention.
	RetentionDays = 30
	// SampleNote is shown with every sample: it is not an exhaustive count.
	SampleNote = "Sampled from the most recent entries in the window (at most 1,000 scanned); counts here describe the sample, not the whole week."
	// RetentionNote explains the out-of-retention result.
	RetentionNote = "Cloud Logging's _Default bucket keeps logs for 30 days; this week is older than that, so request logs are no longer available."
)

// RunRequestsFilter is the Logs Explorer filter for failing Cloud Run requests
// of one service (no time bounds: the Logs Explorer link carries the range).
func RunRequestsFilter(service string, minStatus int) string {
	return fmt.Sprintf(`resource.type="cloud_run_revision" AND resource.labels.service_name=%q AND httpRequest.status>=%d`, service, minStatus)
}

// SamplesFilter is RunRequestsFilter bounded to the window w.
func SamplesFilter(service string, minStatus int, w gcpsource.Window) string {
	return RunRequestsFilter(service, minStatus) + fmt.Sprintf(` AND timestamp>="%s" AND timestamp<"%s"`,
		w.Start.UTC().Format(time.RFC3339), w.End.UTC().Format(time.RFC3339))
}

// LogsExplorerURL is a Google Cloud Logs Explorer deep link for the failing
// requests (status >= 400) of service within w.
func LogsExplorerURL(project, service string, w gcpsource.Window) string {
	enc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	return "https://console.cloud.google.com/logs/query;query=" + enc(RunRequestsFilter(service, 400)) +
		";timeRange=" + w.Start.UTC().Format("2006-01-02T15:04:05.000Z") + "%2F" + w.End.UTC().Format("2006-01-02T15:04:05.000Z") +
		"?project=" + url.QueryEscape(project)
}

// RawEntry is one request-log entry as read from Cloud Logging.
type RawEntry struct {
	Timestamp time.Time
	Status    int
	Method    string
	URL       string // request URL as logged (may include a query string)
	UserAgent string
	RemoteIP  string
	Latency   time.Duration
	Trace     string
}

// EntryIterator yields entries newest-first; Next returns iterator.Done at the end.
type EntryIterator interface {
	Next() (RawEntry, error)
}

// LogEntrySource opens an entry iterator for a project and filter (newest first).
type LogEntrySource interface {
	Entries(ctx context.Context, project, filter string) (EntryIterator, error)
}

// SampleParams selects what to sample.
type SampleParams struct {
	Project   string
	Service   string
	Window    gcpsource.Window
	MinStatus int // default 400
	Limit     int // entries returned; default DefaultSampleLimit
	Now       time.Time
}

// PathStat is one (path, status) group of the sample.
type PathStat struct {
	Path        string `json:"path"`
	Status      int    `json:"status"`
	Count       int    `json:"count"`
	ScannerLike bool   `json:"scanner_like"`
}

// SampleEntry is one returned log entry; the client IP is truncated, never full.
type SampleEntry struct {
	Timestamp   time.Time `json:"timestamp"`
	Status      int       `json:"status"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	UserAgent   string    `json:"user_agent"`
	RemoteIP    string    `json:"remote_ip"`
	LatencyMS   float64   `json:"latency_ms"`
	Trace       string    `json:"trace"`
	ScannerLike bool      `json:"scanner_like"`
}

// SampleResult is the aggregate of one sampling call.
type SampleResult struct {
	Scanned        int            `json:"scanned"`
	Sampled        bool           `json:"sampled"`
	Note           string         `json:"note"`
	OutOfRetention bool           `json:"out_of_retention"`
	RetentionNote  string         `json:"retention_note"`
	StatusCounts   map[string]int `json:"status_counts"`
	TopPaths       []PathStat     `json:"top_paths"`
	Entries        []SampleEntry  `json:"entries"`
	// ScannerShare4xx is the fraction (0..1) of scanned 4xx entries whose path
	// looks like scanner traffic; nil when the sample has no 4xx entries.
	ScannerShare4xx *float64 `json:"scanner_share_4xx"`
	Heuristic       string   `json:"heuristic_note"`
}

// HeuristicNote labels scanner_like as a guess.
const HeuristicNote = "scanner_like is a path-pattern heuristic (dotfiles, wp-*, *.php, /cgi-bin, ...), not proof of who sent the request."

// OutOfRetention reports whether the whole window ended before the logs retention horizon.
func OutOfRetention(w gcpsource.Window, now time.Time) bool {
	return w.End.Before(now.UTC().AddDate(0, 0, -RetentionDays))
}

// SampleErrors reads up to MaxScanned newest log entries of the failing
// requests in the window and aggregates them. A window wholly older than the
// retention horizon returns OutOfRetention with no error and no query.
func SampleErrors(ctx context.Context, src LogEntrySource, p SampleParams) (SampleResult, error) {
	res := SampleResult{Sampled: true, Note: SampleNote, Heuristic: HeuristicNote,
		StatusCounts: map[string]int{}, TopPaths: []PathStat{}, Entries: []SampleEntry{}}
	if p.MinStatus <= 0 {
		p.MinStatus = 400
	}
	if p.Limit <= 0 {
		p.Limit = DefaultSampleLimit
	}
	if p.Limit > MaxSampleLimit {
		p.Limit = MaxSampleLimit
	}
	if !p.Now.IsZero() && OutOfRetention(p.Window, p.Now) {
		res.OutOfRetention = true
		res.RetentionNote = RetentionNote
		return res, nil
	}
	it, err := src.Entries(ctx, p.Project, SamplesFilter(p.Service, p.MinStatus, p.Window))
	if err != nil {
		return res, fmt.Errorf("errorsrc: open log entries: %w", err)
	}
	type key struct {
		path   string
		status int
	}
	counts := map[key]int{}
	var n4, n4scan int
	for res.Scanned < MaxScanned {
		e, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return res, fmt.Errorf("errorsrc: read log entries: %w", err)
		}
		res.Scanned++
		path := StripQuery(e.URL)
		scan := LooksLikeScanner(path)
		res.StatusCounts[strconv.Itoa(e.Status)]++
		counts[key{path, e.Status}]++
		if e.Status >= 400 && e.Status < 500 {
			n4++
			if scan {
				n4scan++
			}
		}
		if len(res.Entries) < p.Limit {
			res.Entries = append(res.Entries, SampleEntry{Timestamp: e.Timestamp.UTC(), Status: e.Status, Method: e.Method, Path: path,
				UserAgent: e.UserAgent, RemoteIP: TruncateIP(e.RemoteIP), LatencyMS: float64(e.Latency) / float64(time.Millisecond),
				Trace: e.Trace, ScannerLike: scan})
		}
	}
	for k, c := range counts {
		res.TopPaths = append(res.TopPaths, PathStat{Path: k.path, Status: k.status, Count: c, ScannerLike: LooksLikeScanner(k.path)})
	}
	sort.Slice(res.TopPaths, func(i, j int) bool {
		a, b := res.TopPaths[i], res.TopPaths[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Status < b.Status
	})
	if len(res.TopPaths) > MaxTopPaths {
		res.TopPaths = res.TopPaths[:MaxTopPaths]
	}
	if n4 > 0 {
		share := float64(n4scan) / float64(n4)
		res.ScannerShare4xx = &share
	}
	if res.Scanned == 0 {
		res.RetentionNote = RetentionNote
	}
	return res, nil
}

// StripQuery returns the path of a logged request URL without scheme, host or query.
func StripQuery(raw string) string {
	if u, err := url.Parse(raw); err == nil && (u.Path != "" || u.Host != "") {
		if u.Path == "" {
			return "/"
		}
		return u.Path
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" {
		return "/"
	}
	return raw
}

// TruncateIP reduces a client IP to its network: IPv4 -> /24, IPv6 -> /48.
// An unparseable address yields "" — a full IP is never returned.
func TruncateIP(s string) string {
	ip := net.ParseIP(strings.TrimSpace(s))
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
}

// ScannerPrefixes are lowercase path prefixes typical of automated probing.
var ScannerPrefixes = []string{
	"/wp-", "/wordpress", "/xmlrpc", "/cgi-bin", "/admin", "/phpmyadmin", "/pma", "/myadmin", "/mysql",
	"/vendor/", "/actuator", "/server-status", "/solr", "/jenkins", "/manager/html", "/boaform", "/hudson",
	"/etc/passwd", "/shell", "/console", "/setup.cgi", "/owa", "/ecp", "/autodiscover", "/telescope", "/_profiler",
}

// ScannerSuffixes are lowercase file extensions that a Cloud Run app of this
// kind does not serve and that scanners request.
var ScannerSuffixes = []string{".php", ".asp", ".aspx", ".jsp", ".cgi", ".bak", ".old", ".orig", ".sql", ".swp", ".zip", ".tar", ".tar.gz", ".7z", ".rar", ".env", ".git", ".ini", ".yml", ".yaml"}

// LooksLikeScanner is a HEURISTIC: it reports whether a request path looks
// like internet-scanner probing (dotfiles/.git/.env, wp-*, *.php, *.bak,
// /cgi-bin, /admin, /phpmyadmin, ...). It never proves who sent the request.
// "/.well-known/" is allowed.
func LooksLikeScanner(path string) bool {
	p := strings.ToLower(path)
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") && seg != ".well-known" && seg != "." && seg != ".." {
			return true
		}
	}
	for _, pre := range ScannerPrefixes {
		if p == pre || strings.HasPrefix(p, pre) || strings.HasPrefix(p, strings.TrimSuffix(pre, "/")+"/") {
			return true
		}
	}
	for _, suf := range ScannerSuffixes {
		if strings.HasSuffix(p, suf) {
			return true
		}
	}
	return false
}

// SampleReader is the real LogEntrySource (one logadmin client per project).
type SampleReader struct{ clients map[string]*logadmin.Client }

// NewSampleReader builds logadmin clients with Application Default Credentials.
func NewSampleReader(ctx context.Context, projects []string) (*SampleReader, func() error, error) {
	g := &SampleReader{clients: map[string]*logadmin.Client{}}
	closeAll := func() error {
		var errs []error
		for _, c := range g.clients {
			errs = append(errs, c.Close())
		}
		return errors.Join(errs...)
	}
	for _, p := range projects {
		p = strings.TrimSpace(p)
		if p == "" || g.clients[p] != nil {
			continue
		}
		c, err := logadmin.NewClient(ctx, p)
		if err != nil {
			_ = closeAll()
			return nil, nil, fmt.Errorf("errorsrc: logadmin client for %s: %w", p, err)
		}
		g.clients[p] = c
	}
	return g, closeAll, nil
}

type gcpEntryIter struct{ it *logadmin.EntryIterator }

// Entries opens a newest-first iterator.
func (g *SampleReader) Entries(ctx context.Context, project, filter string) (EntryIterator, error) {
	c := g.clients[project]
	if c == nil {
		return nil, fmt.Errorf("errorsrc: no logadmin client for project %q", project)
	}
	return gcpEntryIter{it: c.Entries(ctx, logadmin.Filter(filter), logadmin.NewestFirst(), logadmin.PageSize(int32(MaxScanned)))}, nil
}

func (g gcpEntryIter) Next() (RawEntry, error) {
	e, err := g.it.Next()
	if err != nil {
		return RawEntry{}, err
	}
	out := RawEntry{Timestamp: e.Timestamp, Trace: e.Trace}
	if h := e.HTTPRequest; h != nil {
		out.Status, out.RemoteIP, out.Latency = h.Status, h.RemoteIP, h.Latency
		if h.Request != nil {
			out.Method, out.UserAgent = h.Request.Method, h.Request.UserAgent()
			if h.Request.URL != nil {
				out.URL = h.Request.URL.String()
			}
		}
	}
	return out, nil
}
