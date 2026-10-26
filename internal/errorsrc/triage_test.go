package errorsrc

import (
	"strings"
	"testing"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// crRow builds a Cloud Run row with the given total requests, by_code, and
// derives the class counts from by_code (2xx fills the remainder).
func crRow(svc string, requests int64, by map[string]int64) TriageInput {
	var c4, c5 int64
	for c, n := range by {
		switch c[0] {
		case '4':
			c4 += n
		case '5':
			c5 += n
		}
	}
	return TriageInput{Source: gcpsource.ErrSrcCloudRun, Project: "project-a", Service: svc,
		C2xx: i64(requests - c4 - c5), C3xx: i64(0), C4xx: i64(c4), C5xx: i64(c5), ByCode: by}
}

func prevOf(in TriageInput) *TriagePrev { return &TriagePrev{Week: "2026-W42", Row: in} }

func TestTriage_AttentionBoundaries(t *testing.T) {
	cases := []struct {
		name string
		c5   int64
		req  int64
		want string
	}{
		{"5 errors exactly 0.5%", 5, 1000, VerdictNeedsAttention},
		{"4 errors high rate -> watch", 4, 100, VerdictWatch},
		{"5 errors just below 0.5%", 5, 1001, VerdictWatch},
		{"26 of 20000 is 0.13% -> watch", 26, 20000, VerdictWatch},
		{"1 error", 1, 1000000, VerdictWatch},
		{"none", 0, 1000, VerdictHealthy},
	}
	for _, c := range cases {
		by := map[string]int64{}
		if c.c5 > 0 {
			by["502"] = c.c5
		}
		got := Triage(crRow("svc", c.req, by), nil)
		if got.Verdict != c.want {
			t.Errorf("%s: verdict %s want %s (%v)", c.name, got.Verdict, c.want, got.Reasons)
		}
	}
}

func TestTriage_SpikeVsPrev(t *testing.T) {
	prev := crRow("svc", 1000000, map[string]int64{"502": 2})
	cases := []struct {
		c5   int64
		want string
	}{
		{6, VerdictNeedsAttention}, // 3x prev and >= 5
		{5, VerdictWatch},          // < 3x
		{4, VerdictWatch},
	}
	for _, c := range cases {
		got := Triage(crRow("svc", 1000000, map[string]int64{"502": c.c5}), prevOf(prev))
		if got.Verdict != c.want {
			t.Errorf("c5=%d: %s want %s (%v)", c.c5, got.Verdict, c.want, got.Reasons)
		}
	}
	// prev_5xx = 0 never triggers the spike rule.
	got := Triage(crRow("svc", 1000000, map[string]int64{"502": 40}), prevOf(crRow("svc", 1000000, nil)))
	if got.Verdict != VerdictWatch {
		t.Errorf("prev 0: %s", got.Verdict)
	}
	// Exactly 3x with 5 floor: prev 2 -> 6 attention; prev 5 -> 15 attention, 14 watch.
	p5 := crRow("svc", 1000000, map[string]int64{"500": 5})
	if g := Triage(crRow("svc", 1000000, map[string]int64{"500": 15}), prevOf(p5)); g.Verdict != VerdictNeedsAttention {
		t.Errorf("15 vs 5: %s", g.Verdict)
	}
	if g := Triage(crRow("svc", 1000000, map[string]int64{"500": 14}), prevOf(p5)); g.Verdict != VerdictWatch {
		t.Errorf("14 vs 5: %s", g.Verdict)
	}
}

func TestTriage_WatchClient4xx(t *testing.T) {
	cases := []struct {
		name string
		by   map[string]int64
		req  int64
		want string
	}{
		{"25 401s", map[string]int64{"401": 25}, 1000000, VerdictWatch},
		{"24 401s", map[string]int64{"401": 24}, 1000000, VerdictHealthy},
		{"sum of client codes", map[string]int64{"401": 10, "403": 10, "429": 5}, 1000000, VerdictWatch},
		{"rate exactly 2%", map[string]int64{"400": 2}, 100, VerdictWatch},
		{"rate just under 2%", map[string]int64{"400": 2}, 101, VerdictHealthy},
		{"404 is not client", map[string]int64{"404": 49}, 1000000, VerdictHealthy},
	}
	for _, c := range cases {
		got := Triage(crRow("svc", c.req, c.by), nil)
		if got.Verdict != c.want {
			t.Errorf("%s: %s want %s (%v)", c.name, got.Verdict, c.want, got.Reasons)
		}
	}
}

func TestTriage_Noise404(t *testing.T) {
	cases := []struct {
		name string
		by   map[string]int64
		req  int64
		want string
	}{
		{"50 404s", map[string]int64{"404": 50}, 1000000, VerdictNoise},
		{"49 404s", map[string]int64{"404": 49}, 1000000, VerdictHealthy},
		{"rate exactly 5%", map[string]int64{"404": 5}, 100, VerdictNoise},
		{"rate under 5%", map[string]int64{"404": 5}, 101, VerdictHealthy},
		{"404 plus a 5xx is watch", map[string]int64{"404": 500, "500": 1}, 1000000, VerdictWatch},
		{"404 plus client 4xx is watch", map[string]int64{"404": 500, "401": 30}, 1000000, VerdictWatch},
	}
	for _, c := range cases {
		got := Triage(crRow("svc", c.req, c.by), nil)
		if got.Verdict != c.want {
			t.Errorf("%s: %s want %s (%v)", c.name, got.Verdict, c.want, got.Reasons)
		}
	}
	got := Triage(crRow("svc", 1000000, map[string]int64{"404": 11550}), nil)
	if !strings.Contains(strings.Join(got.Reasons, "|"), "11,550 404s — mostly scanner traffic probably; sample to confirm") {
		t.Errorf("reasons = %v", got.Reasons)
	}
}

func TestTriage_GrowthRule(t *testing.T) {
	prev := crRow("svc", 1000000, map[string]int64{"404": 20, "401": 4}) // total 24
	cases := []struct {
		name string
		by   map[string]int64
		want string
	}{
		{"2x but total 48 under 50", map[string]int64{"404": 48}, VerdictHealthy},
		{"2x but under 50", map[string]int64{"404": 40}, VerdictHealthy},
		{"over 50 and over 2x", map[string]int64{"404": 49, "401": 5}, VerdictWatch},
	}
	for _, c := range cases {
		got := Triage(crRow("svc", 1000000, c.by), prevOf(prev))
		if got.Verdict != c.want {
			t.Errorf("%s: %s want %s (%v)", c.name, got.Verdict, c.want, got.Reasons)
		}
	}
	// exactly 50 total vs prev 25 -> watch; 50 vs 26 -> not 2x.
	p25 := crRow("svc", 1000000, map[string]int64{"404": 25})
	if g := Triage(crRow("svc", 1000000, map[string]int64{"404": 49, "405": 1}), prevOf(p25)); g.Verdict != VerdictWatch {
		t.Errorf("50 vs 25: %s %v", g.Verdict, g.Reasons)
	}
	p26 := crRow("svc", 1000000, map[string]int64{"404": 26})
	if g := Triage(crRow("svc", 1000000, map[string]int64{"404": 49, "405": 1}), prevOf(p26)); g.Verdict != VerdictHealthy {
		// 49 404s < 50 and no growth -> healthy
		t.Errorf("50 vs 26: %s %v", g.Verdict, g.Reasons)
	}
}

func TestTriage_NoByCodeUsesClassCounts(t *testing.T) {
	in := TriageInput{Source: gcpsource.ErrSrcCloudRun, Service: "old", C2xx: i64(990), C3xx: i64(0), C4xx: i64(5000), C5xx: i64(100)}
	got := Triage(in, nil)
	if got.Verdict != VerdictNeedsAttention {
		t.Fatalf("verdict = %s", got.Verdict)
	}
	joined := strings.Join(got.Reasons, "|")
	if !strings.Contains(joined, "codes unavailable") && !strings.Contains(joined, "status codes unavailable") {
		t.Errorf("reasons = %v", got.Reasons)
	}
	if got.Rates.RateClient4 != nil || got.Rates.Rate404 != nil || got.Rates.Rate5xx == nil {
		t.Errorf("rates = %+v", got.Rates)
	}
	// 4xx without by_code is skipped (not watch).
	in.C5xx = i64(0)
	if g := Triage(in, nil); g.Verdict != VerdictHealthy {
		t.Errorf("4xx-only no by_code: %s", g.Verdict)
	}
}

func TestTriage_ReasonText(t *testing.T) {
	got := Triage(crRow("svc", 26000, map[string]int64{"502": 26}), nil)
	// 26/26000 = 0.1 %, below 0.5 % -> watch
	if got.Verdict != VerdictWatch || got.Reasons[0] != "26 server errors (502 ×26) = 0.1 % of requests" {
		t.Errorf("%s %v", got.Verdict, got.Reasons)
	}
	if got.Rates.Requests == nil || *got.Rates.Requests != 26000 {
		t.Errorf("rates = %+v", got.Rates)
	}
}

func TestTriage_RatesUnknownAndGap(t *testing.T) {
	g := Triage(TriageInput{Source: gcpsource.ErrSrcCloudRun, Service: "s", Gap: "no data"}, nil)
	if g.Verdict != VerdictHealthy || g.Rates.Requests != nil || g.Rank != 0 {
		t.Errorf("gap = %+v", g)
	}
	z := Triage(TriageInput{Source: gcpsource.ErrSrcLB, C2xx: i64(0), C4xx: i64(0), C5xx: i64(0)}, nil)
	if z.Rates.Rate5xx != nil || z.Verdict != VerdictHealthy || z.Rates.Requests == nil {
		t.Errorf("zero requests = %+v", z)
	}
}

func TestTriage_Change(t *testing.T) {
	prev := crRow("svc", 1000, map[string]int64{"502": 4, "401": 3})
	got := Triage(crRow("svc", 1000, map[string]int64{"502": 8}), prevOf(prev))
	c := got.Change
	if c == nil || c.PrevWeek != "2026-W42" || c.Prev5xx != 4 || c.PrevClient4 == nil || *c.PrevClient4 != 3 || c.Ratio5xx == nil || *c.Ratio5xx != 2 || c.PrevByCode["502"] != 4 {
		t.Fatalf("change = %+v", c)
	}
	if Triage(crRow("svc", 1000, nil), nil).Change != nil {
		t.Error("change must be nil without prev")
	}
	z := Triage(crRow("svc", 1000, map[string]int64{"502": 1}), prevOf(crRow("svc", 1000, nil)))
	if z.Change == nil || z.Change.Ratio5xx != nil || z.Change.PrevClient4 != nil {
		t.Errorf("zero prev change = %+v", z.Change)
	}
}

func TestTriage_ErrorReporting(t *testing.T) {
	er := func(groups, occ int64) TriageInput {
		return TriageInput{Source: gcpsource.ErrSrcErrorReporting, Service: "e", Groups: i64(groups), Occurrences: i64(occ)}
	}
	cases := []struct {
		g, o int64
		want string
	}{
		{0, 0, VerdictHealthy}, {1, 1, VerdictWatch}, {1, 49, VerdictWatch}, {1, 50, VerdictNeedsAttention},
		{4, 10, VerdictWatch}, {5, 10, VerdictNeedsAttention},
	}
	for _, c := range cases {
		if got := Triage(er(c.g, c.o), nil); got.Verdict != c.want {
			t.Errorf("groups=%d occ=%d: %s want %s", c.g, c.o, got.Verdict, c.want)
		}
	}
}

func TestRanksAndSummary(t *testing.T) {
	rows := []TriageInput{
		crRow("healthy", 1000000, nil),
		crRow("noise", 1000000, map[string]int64{"404": 900}),
		crRow("watch-big", 1000000, map[string]int64{"500": 3}),
		crRow("att-small", 1000, map[string]int64{"502": 6}),
		crRow("att-big", 1000, map[string]int64{"503": 20}),
		crRow("noise-bigger", 1000000, map[string]int64{"404": 9000}),
	}
	rs := make([]TriageResult, len(rows))
	for i, r := range rows {
		rs[i] = Triage(r, nil)
	}
	AssignRanks(rs)
	want := map[string]int{"att-big": 1, "att-small": 2, "watch-big": 3, "noise-bigger": 4, "noise": 5, "healthy": 0}
	for i, r := range rows {
		if rs[i].Rank != want[r.Service] {
			t.Errorf("%s rank %d want %d", r.Service, rs[i].Rank, want[r.Service])
		}
	}
	s := Summarize(rs)
	if s.NeedsAttention != 2 || s.Watch != 1 || s.Noise != 2 || s.Healthy != 1 {
		t.Errorf("summary = %+v", s)
	}
	if !strings.Contains(s.Headline, "att-big") || !strings.Contains(s.Headline, "503") {
		t.Errorf("headline = %q", s.Headline)
	}

	rs = []TriageResult{Triage(crRow("a", 1000000, map[string]int64{"404": 900}), nil), Triage(crRow("b", 1000000, map[string]int64{"404": 900}), nil), Triage(crRow("c", 1000, nil), nil)}
	AssignRanks(rs)
	if h := Summarize(rs).Headline; h != "Nothing needs attention this week — 2 services look like scanner noise." {
		t.Errorf("headline = %q", h)
	}
	// Only a watch row: headline names it.
	rs = []TriageResult{Triage(crRow("w", 1000000, map[string]int64{"401": 40}), nil)}
	AssignRanks(rs)
	if h := Summarize(rs).Headline; !strings.Contains(h, "w") || !strings.Contains(h, "401") {
		t.Errorf("watch headline = %q", h)
	}
}
