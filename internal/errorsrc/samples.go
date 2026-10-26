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
	"unicode/utf8"

	logging "cloud.google.com/go/logging/apiv2"
	"cloud.google.com/go/logging/apiv2/loggingpb"
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

// Focus values of GET /errors/samples: which class of failing requests to read.
const (
	FocusAll      = "all"
	FocusServer   = "server"   // status >= 500
	FocusClient   = "client"   // 400, 401, 403, 405, 422, 429
	FocusNotFound = "notfound" // 404
)

// ClientErrorCodes are the 4xx codes that may be real client/application problems (404 is separate).
var ClientErrorCodes = []int{400, 401, 403, 405, 422, 429}

// ValidFocus reports whether f is a known focus ("" counts as all).
func ValidFocus(f string) bool {
	switch f {
	case "", FocusAll, FocusServer, FocusClient, FocusNotFound:
		return true
	}
	return false
}

// statusClause is the server-side Logging filter clause for a focus; minStatus
// applies only to FocusAll.
func statusClause(focus string, minStatus int) string {
	switch focus {
	case FocusServer:
		return "httpRequest.status>=500"
	case FocusNotFound:
		return "httpRequest.status=404"
	case FocusClient:
		parts := make([]string, len(ClientErrorCodes))
		for i, c := range ClientErrorCodes {
			parts[i] = fmt.Sprintf("httpRequest.status=%d", c)
		}
		return "(" + strings.Join(parts, " OR ") + ")"
	}
	return fmt.Sprintf("httpRequest.status>=%d", minStatus)
}

// FocusFilter is the Logs Explorer filter (no time bounds) for one service and focus.
func FocusFilter(service, focus string, minStatus int) string {
	return fmt.Sprintf(`resource.type="cloud_run_revision" AND resource.labels.service_name=%q AND `, service) + statusClause(focus, minStatus)
}

// SamplesFilter is RunRequestsFilter bounded to the window w.
func SamplesFilter(service string, minStatus int, w gcpsource.Window) string {
	return FocusSamplesFilter(service, FocusAll, minStatus, w)
}

// FocusSamplesFilter is FocusFilter bounded to the window w.
func FocusSamplesFilter(service, focus string, minStatus int, w gcpsource.Window) string {
	return FocusFilter(service, focus, minStatus) + fmt.Sprintf(` AND timestamp>="%s" AND timestamp<"%s"`,
		w.Start.UTC().Format(time.RFC3339), w.End.UTC().Format(time.RFC3339))
}

// FocusLogsExplorerURL is LogsExplorerURL restricted to a focus.
func FocusLogsExplorerURL(project, service, focus string, minStatus int, w gcpsource.Window) string {
	enc := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	return "https://console.cloud.google.com/logs/query;query=" + enc(FocusFilter(service, focus, minStatus)) +
		";timeRange=" + w.Start.UTC().Format("2006-01-02T15:04:05.000Z") + "%2F" + w.End.UTC().Format("2006-01-02T15:04:05.000Z") +
		"?project=" + url.QueryEscape(project)
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
	MinStatus int    // default 400; used only when Focus is all
	Focus     string // server | client | notfound | all (default)
	Limit     int    // entries returned; default DefaultSampleLimit
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
	// Focus echoes the requested focus (all when unset).
	Focus string `json:"focus"`
	// Unparsed counts scanned entries whose request URL was malformed (kept, with a best-effort path).
	Unparsed int `json:"unparsed"`
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
		Focus: p.Focus, StatusCounts: map[string]int{}, TopPaths: []PathStat{}, Entries: []SampleEntry{}}
	if res.Focus == "" {
		res.Focus = FocusAll
	}
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
	it, err := src.Entries(ctx, p.Project, FocusSamplesFilter(p.Service, res.Focus, p.MinStatus, p.Window))
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
		path, ok := ParsePath(e.URL)
		if !ok {
			res.Unparsed++
		}
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

// MaxPathLen caps a returned request path.
const MaxPathLen = 200

// StripQuery returns the path of a logged request URL without scheme, host or query.
func StripQuery(raw string) string {
	p, _ := ParsePath(raw)
	return p
}

// ParsePath extracts the request path from a logged URL, leniently: it never
// fails. ok is false when the URL was malformed (url.Parse failed or the path
// is not valid percent-encoding); the path is then a best effort and keeps the
// raw text where it cannot be decoded. Paths are truncated to MaxPathLen.
func ParsePath(raw string) (path string, ok bool) {
	if raw == "" {
		return "/", true
	}
	if u, err := url.Parse(raw); err == nil && (u.Path != "" || u.Host != "") {
		if u.Path == "" {
			return "/", true
		}
		return truncatePath(u.Path), true
	}
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 && !strings.ContainsAny(rest[:i], "/?#") {
		rest = rest[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[j:]
		} else {
			rest = ""
		}
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	if rest == "" {
		return "/", false
	}
	if dec, err := url.PathUnescape(rest); err == nil {
		return truncatePath(dec), false
	}
	return truncatePath(rest), false
}

func truncatePath(p string) string {
	if len(p) <= MaxPathLen {
		return p
	}
	p = p[:MaxPathLen]
	for !utf8.ValidString(p) && len(p) > 0 {
		p = p[:len(p)-1]
	}
	return p
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

// SampleReader is the real LogEntrySource. It uses the raw Logging API
// (ListLogEntries) so httpRequest.requestUrl stays a plain string: the
// logadmin wrapper runs net/url.Parse on it and fails the whole read on one
// malformed scanner URL.
type SampleReader struct{ client *logging.Client }

// NewSampleReader builds a Logging client with Application Default Credentials.
// The projects argument is kept for call compatibility; projects are chosen per call.
func NewSampleReader(ctx context.Context, projects []string) (*SampleReader, func() error, error) {
	c, err := logging.NewClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("errorsrc: logging client: %w", err)
	}
	return &SampleReader{client: c}, c.Close, nil
}

type gcpEntryIter struct{ it *logging.LogEntryIterator }

// Entries opens a newest-first iterator.
func (g *SampleReader) Entries(ctx context.Context, project, filter string) (EntryIterator, error) {
	if strings.TrimSpace(project) == "" {
		return nil, errors.New("errorsrc: empty project for log entries")
	}
	it := g.client.ListLogEntries(ctx, &loggingpb.ListLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        filter,
		OrderBy:       "timestamp desc",
		PageSize:      int32(MaxScanned),
	})
	return gcpEntryIter{it: it}, nil
}

func (g gcpEntryIter) Next() (RawEntry, error) {
	e, err := g.it.Next()
	if err != nil {
		return RawEntry{}, err
	}
	out := RawEntry{Trace: e.GetTrace()}
	if ts := e.GetTimestamp(); ts != nil {
		out.Timestamp = ts.AsTime()
	}
	if h := e.GetHttpRequest(); h != nil {
		out.Status, out.RemoteIP = int(h.GetStatus()), h.GetRemoteIp()
		out.Method, out.UserAgent, out.URL = h.GetRequestMethod(), h.GetUserAgent(), h.GetRequestUrl()
		if l := h.GetLatency(); l != nil {
			out.Latency = l.AsDuration()
		}
	}
	return out, nil
}
