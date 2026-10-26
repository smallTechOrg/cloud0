package errorsrc

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// Verdict values of the Errors-tab triage.
const (
	VerdictNeedsAttention = "needs_attention"
	VerdictWatch          = "watch"
	VerdictNoise          = "noise"
	VerdictHealthy        = "healthy"
)

// Triage thresholds. Every rule is evaluated by Triage; boundaries are
// inclusive (>=) and unit-tested.
//
// needs_attention (request-class rows: Cloud Run, load balancer, nginx):
//   - 5xx >= AttentionMin5xx AND rate_5xx >= AttentionRate5xx, OR
//   - a previous week exists with prev_5xx > 0 AND 5xx >= SpikeFactor5xx x prev_5xx
//     AND 5xx >= AttentionMin5xx.
//
// watch: non-404 client errors (400,401,403,405,422,429; needs by_code)
// >= WatchClient4xxCount or rate_client_4xx >= WatchClient4xxRate; any 5xx
// below the attention bar; or a previous week exists and 4xx+5xx >=
// GrowthFactorTotal x previous AND >= GrowthMinTotal.
//
// noise: the only elevated class is 404 (404 >= NoiseMin404 or rate_404 >=
// NoiseRate404). healthy otherwise.
//
// Error Reporting rows: occurrences > 0 is watch; occurrences >=
// ERAttentionOccurrences or groups >= ERAttentionGroups is needs_attention.
const (
	AttentionMin5xx          = 5
	AttentionRate5xx         = 0.005
	SpikeFactor5xx           = 3
	WatchClient4xxCount      = 25
	WatchClient4xxRate       = 0.02
	GrowthFactorTotal        = 2
	GrowthMinTotal           = 50
	NoiseMin404              = 50
	NoiseRate404             = 0.05
	ERAttentionOccurrences   = 50
	ERAttentionGroups        = 5
	codesUnavailableReason   = "exact status codes unavailable until this week is refreshed"
	scannerReason404Fragment = "mostly scanner traffic probably; sample to confirm"
)

// clientCodes are the non-404 4xx codes counted as "client errors".
var clientCodes = []string{"400", "401", "403", "405", "422", "429"}

// TriageInput is one Errors-table row as the triage sees it.
type TriageInput struct {
	Source      string
	Project     string
	Service     string
	C2xx, C3xx  *int64
	C4xx, C5xx  *int64
	Groups      *int64
	Occurrences *int64
	ByCode      map[string]int64
	Gap         string
}

// TriageInputOf adapts a stored row.
func TriageInputOf(r gcpsource.ErrorRow) TriageInput {
	return TriageInput{Source: r.Source, Project: r.Project, Service: r.Service, C2xx: r.C2xx, C3xx: r.C3xx,
		C4xx: r.C4xx, C5xx: r.C5xx, Groups: r.Groups, Occurrences: r.Occurrences, ByCode: r.ByCode, Gap: r.Gap}
}

// TriageRates are fractions 0..1; nil when requests are unknown (or zero),
// and rate_client_4xx / rate_404 also nil when by_code is absent.
type TriageRates struct {
	Requests    *int64   `json:"requests"`
	Rate5xx     *float64 `json:"rate_5xx"`
	RateClient4 *float64 `json:"rate_client_4xx"`
	Rate404     *float64 `json:"rate_404"`
}

// TriageChange compares against the same service in the prior ISO week.
type TriageChange struct {
	PrevWeek    string           `json:"prev_week"`
	Prev5xx     int64            `json:"prev_5xx"`
	PrevClient4 *int64           `json:"prev_client_4xx"`
	PrevByCode  map[string]int64 `json:"prev_by_code"`
	Ratio5xx    *float64         `json:"ratio_5xx"` // 5xx / prev_5xx; nil when prev_5xx == 0
}

// TriageResult is the verdict for one row.
type TriageResult struct {
	Verdict string        `json:"verdict"`
	Reasons []string      `json:"reasons"`
	Rank    int           `json:"rank"`
	Rates   TriageRates   `json:"rates"`
	Change  *TriageChange `json:"change"`

	// internal ordering / headline material
	sortPrimary, sortSecondary int64
	dominant                   string
	input                      TriageInput
}

// TriagePrev is the prior week's row for the same service plus its week id.
type TriagePrev struct {
	Week string
	Row  TriageInput
}

func val(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func sumCodes(by map[string]int64, codes []string) int64 {
	var n int64
	for _, c := range codes {
		n += by[c]
	}
	return n
}

// codesOfClass lists by_code entries of a class prefix ("5"), highest first.
func codesOfClass(by map[string]int64, prefix string, skip404 bool) []string {
	var out []string
	for c, n := range by {
		if n > 0 && strings.HasPrefix(c, prefix) && !(skip404 && c == "404") {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if by[out[i]] != by[out[j]] {
			return by[out[i]] > by[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

func fmtInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

func fmtPct(f float64) string {
	p := f * 100
	if p > 0 && p < 0.1 {
		return "<0.1 %"
	}
	return fmt.Sprintf("%.1f %%", p)
}

func codeList(by map[string]int64, codes []string, limit int) string {
	var parts []string
	for i, c := range codes {
		if i == limit {
			break
		}
		parts = append(parts, fmt.Sprintf("%s ×%s", c, fmtInt(by[c])))
	}
	return strings.Join(parts, ", ")
}

// requestClass reports whether a source is judged from HTTP status classes.
func requestClass(source string) bool { return source != gcpsource.ErrSrcErrorReporting }

// Triage judges one row. prev is the same service's row from the prior ISO
// week, or nil when none is stored (then Change is nil and the growth rules
// do not apply). Gap rows are healthy with a "no data" reason.
func Triage(in TriageInput, prev *TriagePrev) TriageResult {
	res := TriageResult{Reasons: []string{}, input: in}
	if in.Gap != "" {
		res.Verdict = VerdictHealthy
		res.Reasons = []string{"No data for this row (" + in.Gap + ")."}
		return res
	}
	if !requestClass(in.Source) {
		triageGroups(&res, in)
		return res
	}
	c5, c4 := val(in.C5xx), val(in.C4xx)
	var req int64
	reqKnown := in.C2xx != nil || in.C3xx != nil || in.C4xx != nil || in.C5xx != nil
	req = val(in.C2xx) + val(in.C3xx) + c4 + c5
	haveCodes := len(in.ByCode) > 0
	var client, n404 int64
	if haveCodes {
		client = sumCodes(in.ByCode, clientCodes)
		n404 = in.ByCode["404"]
	}
	res.Rates = TriageRates{}
	if reqKnown {
		r := req
		res.Rates.Requests = &r
	}
	frac := func(n int64) *float64 {
		if !reqKnown || req <= 0 {
			return nil
		}
		f := float64(n) / float64(req)
		return &f
	}
	res.Rates.Rate5xx = frac(c5)
	if haveCodes {
		res.Rates.RateClient4 = frac(client)
		res.Rates.Rate404 = frac(n404)
	}
	reqPos := reqKnown && req > 0
	// Integer cross-multiplied threshold checks avoid float boundary error.
	rate5Hit := reqPos && c5*200 >= req // rate_5xx >= 0.005 (AttentionRate5xx)
	clientRateHit := haveCodes && reqPos && client*50 >= req
	rate404Hit := haveCodes && reqPos && n404*20 >= req

	var prevOK bool
	var prev5, prevTotal int64
	if prev != nil && prev.Row.Gap == "" && prev.Row.C5xx != nil {
		prevOK = true
		prev5 = val(prev.Row.C5xx)
		prevTotal = prev5 + val(prev.Row.C4xx)
		ch := &TriageChange{PrevWeek: prev.Week, Prev5xx: prev5}
		if len(prev.Row.ByCode) > 0 {
			pc := sumCodes(prev.Row.ByCode, clientCodes)
			ch.PrevClient4 = &pc
			ch.PrevByCode = prev.Row.ByCode
		}
		if prev5 > 0 {
			f := float64(c5) / float64(prev5)
			ch.Ratio5xx = &f
		}
		res.Change = ch
	}

	spike := prevOK && prev5 > 0 && c5 >= SpikeFactor5xx*prev5 && c5 >= AttentionMin5xx
	attention := (c5 >= AttentionMin5xx && rate5Hit) || spike
	growth := prevOK && c4+c5 >= GrowthFactorTotal*prevTotal && c4+c5 >= GrowthMinTotal
	watchClient := haveCodes && (client >= WatchClient4xxCount || clientRateHit)
	watch5 := c5 > 0 && !attention
	noise := haveCodes && (n404 >= NoiseMin404 || rate404Hit)

	switch {
	case attention:
		res.Verdict = VerdictNeedsAttention
	case watchClient || watch5 || growth:
		res.Verdict = VerdictWatch
	case noise:
		res.Verdict = VerdictNoise
	default:
		res.Verdict = VerdictHealthy
	}

	// Reasons.
	if c5 > 0 {
		s := fmt.Sprintf("%s server errors", fmtInt(c5))
		if haveCodes {
			if l := codeList(in.ByCode, codesOfClass(in.ByCode, "5", false), 3); l != "" {
				s += " (" + l + ")"
			}
		}
		if f := res.Rates.Rate5xx; f != nil {
			s += " = " + fmtPct(*f) + " of requests"
		}
		res.Reasons = append(res.Reasons, s)
	}
	if spike {
		res.Reasons = append(res.Reasons, fmt.Sprintf("5xx up %.1f× vs last week (%s → %s)", float64(c5)/float64(prev5), fmtInt(prev5), fmtInt(c5)))
	}
	if client > 0 && (watchClient || res.Verdict == VerdictWatch) {
		s := fmt.Sprintf("%s client errors (%s)", fmtInt(client), codeList(in.ByCode, clientCodes2(in.ByCode), 3))
		if f := res.Rates.RateClient4; f != nil {
			s += " = " + fmtPct(*f) + " of requests"
		}
		res.Reasons = append(res.Reasons, s)
	}
	if growth {
		res.Reasons = append(res.Reasons, fmt.Sprintf("4xx+5xx errors grew from %s to %s vs last week", fmtInt(prevTotal), fmtInt(c4+c5)))
	}
	if noise && n404 > 0 {
		res.Reasons = append(res.Reasons, fmt.Sprintf("%s 404s — %s", fmtInt(n404), scannerReason404Fragment))
	}
	if !haveCodes && (c5 > 0 || c4 > 0) {
		res.Reasons = append(res.Reasons, codesUnavailableReason)
	}
	if len(res.Reasons) == 0 {
		res.Reasons = append(res.Reasons, "No elevated errors this week.")
	}

	res.sortPrimary = c5
	res.sortSecondary = client
	if res.Verdict == VerdictNoise {
		res.sortPrimary = n404
	}
	if c5 > 0 && haveCodes {
		if cs := codesOfClass(in.ByCode, "5", false); len(cs) > 0 {
			res.dominant = cs[0]
		}
	} else if client > 0 {
		if cs := clientCodes2(in.ByCode); len(cs) > 0 {
			res.dominant = cs[0]
		}
	}
	return res
}

// clientCodes2 returns the present client codes, highest first.
func clientCodes2(by map[string]int64) []string {
	var out []string
	for _, c := range clientCodes {
		if by[c] > 0 {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return by[out[i]] > by[out[j]] })
	return out
}

func triageGroups(res *TriageResult, in TriageInput) {
	occ, groups := val(in.Occurrences), val(in.Groups)
	switch {
	case occ >= ERAttentionOccurrences || groups >= ERAttentionGroups:
		res.Verdict = VerdictNeedsAttention
	case occ > 0:
		res.Verdict = VerdictWatch
	default:
		res.Verdict = VerdictHealthy
		res.Reasons = append(res.Reasons, "No error groups reported this week.")
		return
	}
	res.Reasons = append(res.Reasons, fmt.Sprintf("%s error occurrences in %s groups reported by Error Reporting", fmtInt(occ), fmtInt(groups)))
	res.sortPrimary = occ
	res.sortSecondary = groups
}

// AssignRanks sets Rank in place: needs_attention first, then watch, then
// noise, each by 5xx count (Error Reporting: occurrences) descending, ties by
// client-error count then service name. Healthy rows get rank 0.
func AssignRanks(rs []TriageResult) {
	order := map[string]int{VerdictNeedsAttention: 0, VerdictWatch: 1, VerdictNoise: 2}
	idx := make([]int, 0, len(rs))
	for i := range rs {
		rs[i].Rank = 0
		if _, ok := order[rs[i].Verdict]; ok {
			idx = append(idx, i)
		}
	}
	sort.SliceStable(idx, func(a, b int) bool {
		x, y := rs[idx[a]], rs[idx[b]]
		if order[x.Verdict] != order[y.Verdict] {
			return order[x.Verdict] < order[y.Verdict]
		}
		if x.sortPrimary != y.sortPrimary {
			return x.sortPrimary > y.sortPrimary
		}
		if x.sortSecondary != y.sortSecondary {
			return x.sortSecondary > y.sortSecondary
		}
		return x.input.Service < y.input.Service
	})
	for n, i := range idx {
		rs[i].Rank = n + 1
	}
}

// TriageSummary counts verdicts and writes the one-line headline.
type TriageSummary struct {
	NeedsAttention int    `json:"needs_attention"`
	Watch          int    `json:"watch"`
	Noise          int    `json:"noise"`
	Healthy        int    `json:"healthy"`
	Headline       string `json:"headline"`
}

// Summarize counts verdicts (ranks must already be assigned) and builds the headline.
func Summarize(rs []TriageResult) TriageSummary {
	var s TriageSummary
	var top *TriageResult
	for i := range rs {
		switch rs[i].Verdict {
		case VerdictNeedsAttention:
			s.NeedsAttention++
		case VerdictWatch:
			s.Watch++
		case VerdictNoise:
			s.Noise++
		default:
			s.Healthy++
		}
		if rs[i].Rank == 1 {
			top = &rs[i]
		}
	}
	if top != nil && top.Verdict != VerdictNoise {
		s.Headline = headlineFor(*top)
	} else if s.Noise > 0 {
		s.Headline = fmt.Sprintf("Nothing needs attention this week — %d %s like scanner noise.", s.Noise, plural(s.Noise, "service looks", "services look"))
	} else {
		s.Headline = "Nothing needs attention this week — all services look healthy."
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func headlineFor(r TriageResult) string {
	in := r.input
	lead := "Look first at "
	if r.Verdict == VerdictWatch {
		lead = "Worth watching: "
	}
	name := in.Service
	if name == "" {
		name = in.Source
	}
	if !requestClass(in.Source) {
		return fmt.Sprintf("%s%s — %s error occurrences in %s groups.", lead, name, fmtInt(val(in.Occurrences)), fmtInt(val(in.Groups)))
	}
	if c5 := val(in.C5xx); c5 > 0 {
		if r.dominant != "" {
			return fmt.Sprintf("%s%s — %s server errors, mostly %s.", lead, name, fmtInt(c5), r.dominant)
		}
		return fmt.Sprintf("%s%s — %s server errors.", lead, name, fmtInt(c5))
	}
	if r.dominant != "" {
		return fmt.Sprintf("%s%s — client errors, mostly %s.", lead, name, r.dominant)
	}
	return fmt.Sprintf("%s%s — errors grew vs last week.", lead, name)
}
