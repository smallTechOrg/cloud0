package attribution

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// Seeder thresholds. A name token becomes a product only when it recurs on at
// least SeedMinResources distinct resources, or sits on one resource carrying
// at least SeedMinCostShare of the week's positive net.
const (
	SeedMinResources = 2
	SeedMinCostShare = 0.05
	// seedMaxPer* cap how many rules each family can emit (tier width).
	seedMaxLabel     = 100
	seedMaxProject   = 100
	seedMaxAlias     = 80
	seedMaxProduct   = 100
	seedTopUnmatched = 20
	seedMaxExamples  = 3
)

var seedNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// SeedRuleStat describes one proposed rule; RuleStats is parallel to
// SeedResult.Proposed. Rows/Net count the rows the rule actually DECIDES in the
// final rule set (first matching rule of its dimension).
type SeedRuleStat struct {
	Rows        int     `json:"rows"`
	Resources   int     `json:"resources"`
	Net         float64 `json:"net"`
	Explanation string  `json:"explanation"`
}

// UnmatchedResource is a top resource still unattributed after seeding.
type UnmatchedResource struct {
	ProjectID    string  `json:"project_id"`
	ResourceName string  `json:"resource_name"`
	Service      string  `json:"service"`
	SKU          string  `json:"sku"`
	Net          float64 `json:"net"`
}

// SeedAnalysis is the full seeder output: the frozen SeedResult plus coverage
// before/after, per-rule statistics and the top unmatched resources.
type SeedAnalysis struct {
	Result         SeedResult
	CoverageBefore Coverage // opts.Existing as stored
	CoverageAfter  Coverage // existing non-seed rules + Proposed
	RuleStats      []SeedRuleStat
	Unmatched      []UnmatchedResource
}

// NewSeeder returns the deterministic seeder.
func NewSeeder() Seeder { return seeder{} }

type seeder struct{}

func (seeder) Seed(rows []CostRow, opts SeedOptions) (SeedResult, error) {
	a, err := AnalyzeSeed(rows, opts)
	return a.Result, err
}

// ---- local minimal seedEvaluator (coverage only) --------------------------------

type seedWRule struct {
	Rule
	pre     []string // required-substring prefilter (any of); nil = none
	auto    bool     // prunable when it decides nothing
	explain string
	re      *regexp.Regexp
	val     string
	src     int // index in the proposed list, -1 for existing rules
}

type seedPrepRow struct {
	project, service, sku, name, global string
	labels                              map[string]string
	net                                 float64
}

type seedVerdict struct {
	product  string
	shared   bool
	env      string
	prodRule int // index into seedEvaluator.rules, -1 none, -2 builtin
	envRule  int
}

type seedEvaluator struct{ rules []*seedWRule }

func newSeedEvaluator(ws []*seedWRule) *seedEvaluator {
	rs := make([]*seedWRule, 0, len(ws))
	for _, w := range ws {
		if !w.Enabled {
			continue
		}
		w.val = strings.ToLower(w.Match.Value)
		w.re = nil
		if w.Match.Op == OpRegex {
			re, err := regexp.Compile("(?i)" + w.Match.Value)
			if err != nil {
				continue
			}
			w.re = re
		}
		rs = append(rs, w)
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Priority < rs[j].Priority })
	return &seedEvaluator{rules: rs}
}

func seedPrep(r CostRow) seedPrepRow {
	p := seedPrepRow{
		project: strings.ToLower(r.ProjectID), service: strings.ToLower(r.Service), sku: strings.ToLower(r.SKU),
		name: strings.ToLower(r.ResourceName), global: strings.ToLower(r.ResourceGlobalName), net: r.Net,
	}
	if len(r.Labels) > 0 {
		p.labels = make(map[string]string, len(r.Labels))
		for k, v := range r.Labels {
			p.labels[strings.ToLower(strings.TrimSpace(k))] = strings.ToLower(strings.TrimSpace(v))
		}
	}
	return p
}

func seedOpMatch(w *seedWRule, s string) bool {
	if s == "" {
		return false
	}
	switch w.Match.Op {
	case OpEquals:
		return s == w.val
	case OpContains:
		return strings.Contains(s, w.val)
	case OpPrefix:
		return strings.HasPrefix(s, w.val)
	case OpRegex:
		return w.re != nil && w.re.MatchString(s)
	}
	return false
}

func (w *seedWRule) matches(p *seedPrepRow) bool {
	switch w.Match.Field {
	case FieldLabel:
		v, ok := p.labels[strings.ToLower(w.Match.Key)]
		return ok && seedOpMatch(w, v)
	case FieldProject:
		return seedOpMatch(w, p.project)
	case FieldService:
		return seedOpMatch(w, p.service)
	case FieldSKU:
		return seedOpMatch(w, p.sku)
	case FieldResourceName:
		if p.name == "" && p.global == "" {
			return false
		}
		if len(w.pre) > 0 {
			hit := false
			for _, s := range w.pre {
				if strings.Contains(p.name, s) || strings.Contains(p.global, s) {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		}
		return seedOpMatch(w, p.name) || seedOpMatch(w, p.global)
	}
	return false
}

func (e *seedEvaluator) eval(p *seedPrepRow) seedVerdict {
	v := seedVerdict{prodRule: -1, envRule: -1}
	prodDone, envDone := false, false
	if pl := p.labels[ProductLabelKey]; pl != "" {
		prodDone, v.prodRule = true, -2
		if pl == SharedLabelValue {
			v.shared = true
		} else {
			v.product = pl
		}
	}
	for _, k := range EnvLabelKeys {
		if el := p.labels[k]; el != "" {
			envDone, v.envRule, v.env = true, -2, el
			break
		}
	}
	for i, w := range e.rules {
		if prodDone && envDone {
			break
		}
		carriesProd := w.Action.Product != "" || w.Action.Shared
		carriesEnv := w.Action.Environment != ""
		if (prodDone || !carriesProd) && (envDone || !carriesEnv) {
			continue
		}
		if !w.matches(p) {
			continue
		}
		if !prodDone && carriesProd {
			prodDone, v.prodRule = true, i
			v.shared = w.Action.Shared
			v.product = strings.ToLower(w.Action.Product)
		}
		if !envDone && carriesEnv {
			envDone, v.envRule, v.env = true, i, strings.ToLower(w.Action.Environment)
		}
	}
	return v
}

func seedPct(x float64) float64 { return math.Round(x*100) / 100 }

func seedCoverageOf(ps []seedPrepRow, vs []seedVerdict) Coverage {
	var c Coverage
	for i := range ps {
		c.TotalNet += ps[i].net
		switch {
		case vs[i].shared:
			c.SharedNet += ps[i].net
		case vs[i].product != "":
			c.AttributedNet += ps[i].net
		default:
			c.UnattributedNet += ps[i].net
		}
	}
	if c.TotalNet == 0 {
		return Coverage{}
	}
	c.AttributedPct = seedPct(100 * c.AttributedNet / c.TotalNet)
	c.SharedPct = seedPct(100 * c.SharedNet / c.TotalNet)
	c.UnattributedPct = seedPct(100 - c.AttributedPct - c.SharedPct)
	return c
}

func seedEvalAll(e *seedEvaluator, ps []seedPrepRow) []seedVerdict {
	vs := make([]seedVerdict, len(ps))
	for i := range ps {
		vs[i] = e.eval(&ps[i])
	}
	return vs
}

// ---- seeding ----------------------------------------------------------------

type seedRes struct {
	key, project, name string
	net                float64
	uni, bi            map[string]bool
	envs               map[string]bool
}

type seedTok struct {
	tok  string
	res  []*seedRes
	net  float64
	bi   bool
	keep bool
}

func seedSanitizeName(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, seedNameRe.MatchString(s)
}

func seedNormMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		k = strings.ToLower(strings.TrimSpace(k))
		if nv, ok := seedSanitizeName(v); ok && k != "" {
			out[k] = nv
		}
	}
	return out
}

func seedExamples(rs []*seedRes) string {
	s := append([]*seedRes(nil), rs...)
	sort.Slice(s, func(i, j int) bool {
		if s[i].net != s[j].net {
			return s[i].net > s[j].net
		}
		return s[i].key < s[j].key
	})
	var names []string
	for i, r := range s {
		if i == seedMaxExamples {
			names = append(names, "…")
			break
		}
		names = append(names, r.name)
	}
	return strings.Join(names, ", ")
}

func seedTokenRegex(tok string) (string, string) {
	parts := strings.Split(tok, "-")
	return `(^|[^a-z])` + strings.Join(parts, `[-_.]?`) + `($|[^a-z])`, parts[0]
}

func seedRuleKey(r Rule) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%t", r.Match.Field, r.Match.Op, strings.ToLower(r.Match.Key),
		strings.ToLower(r.Match.Value), r.Action.Product, r.Action.Environment, "", r.Action.Shared)
}

// AnalyzeSeed derives the seed rule set plus coverage and diagnostics. It is
// pure: equal rows (in any order) and options give identical output.
func AnalyzeSeed(rows []CostRow, opts SeedOptions) (SeedAnalysis, error) {
	aliases := seedNormMap(opts.Aliases)

	// Aggregate resources and label observations.
	resMap := map[string]*seedRes{}
	var totalPos float64
	type lv struct{ key, value string }
	labelRes := map[lv]map[string]*seedRes{}
	labelRows := map[lv]int{}
	var sharedSKU struct {
		fwd, vmm, ar, minimum int
		fwdSKU, minSKU        string
	}
	for _, r := range rows {
		if r.Net > 0 {
			totalPos += r.Net
		}
		name := strings.ToLower(strings.TrimSpace(r.ResourceName))
		var ra *seedRes
		if name != "" {
			k := strings.ToLower(r.ProjectID) + "\x00" + name
			ra = resMap[k]
			if ra == nil {
				ra = &seedRes{key: k, project: strings.ToLower(r.ProjectID), name: name, uni: map[string]bool{}, bi: map[string]bool{}, envs: map[string]bool{}}
				resMap[k] = ra
			}
			ra.net += r.Net
		}
		for k, v := range r.Labels {
			k, v = strings.ToLower(strings.TrimSpace(k)), strings.ToLower(strings.TrimSpace(v))
			if v == "" || (k != "product" && k != "app") {
				continue
			}
			x := lv{k, v}
			labelRows[x]++
			if ra != nil {
				if labelRes[x] == nil {
					labelRes[x] = map[string]*seedRes{}
				}
				labelRes[x][ra.key] = ra
			}
		}
		svc, sku := strings.ToLower(r.Service), strings.ToLower(r.SKU)
		if strings.Contains(sku, "forwarding rule") {
			sharedSKU.fwd++
		}
		if svc == "vm manager" {
			sharedSKU.vmm++
		}
		if svc == "artifact registry" {
			sharedSKU.ar++
		}
		if strings.Contains(sku, "minimum") && strings.Contains(svc, "network") {
			sharedSKU.minimum++
		}
	}
	resList := make([]*seedRes, 0, len(resMap))
	for _, ra := range resMap {
		resList = append(resList, ra)
	}
	sort.Slice(resList, func(i, j int) bool { return resList[i].key < resList[j].key })

	// Tokenise.
	for _, ra := range resList {
		segs := Tokenize(ra.name)
		for i, s := range segs {
			if env, ok := EnvForToken(s); ok {
				ra.envs[env] = true
				continue
			}
			if len(s) >= 3 && !IsStopToken(s) {
				ra.uni[s] = true
			}
			if i+1 < len(segs) && len(s) >= 3 && !IsStopToken(s) {
				n := segs[i+1]
				if _, isEnv := EnvForToken(n); !isEnv && !IsStopToken(n) {
					ra.bi[s+"-"+n] = true
				}
			}
		}
	}
	toks := map[string]*seedTok{}
	for _, ra := range resList {
		for t := range ra.uni {
			seedAddTok(toks, t, ra, false)
		}
		for t := range ra.bi {
			seedAddTok(toks, t, ra, true)
		}
	}
	qualifies := func(t *seedTok) bool {
		if len(t.res) >= SeedMinResources {
			return true
		}
		for _, r := range t.res {
			if totalPos > 0 && r.net/totalPos >= SeedMinCostShare {
				return true
			}
		}
		return false
	}
	for _, t := range toks {
		t.keep = qualifies(t) && len(t.tok) <= 40
	}
	// Unigram shared by >=2 qualifying bigrams is a prefix/suffix, not a product.
	biCount := map[string]int{}
	for _, t := range toks {
		if t.bi && t.keep {
			for _, p := range strings.Split(t.tok, "-") {
				biCount[p]++
			}
		}
	}
	for _, t := range toks {
		if !t.bi && t.keep && biCount[t.tok] >= 2 {
			t.keep = false
		}
	}
	// A bigram is redundant when one of its components survives as a unigram.
	for _, t := range toks {
		if t.bi && t.keep {
			for _, p := range strings.Split(t.tok, "-") {
				if u := toks[p]; u != nil && !u.bi && u.keep {
					t.keep = false
				}
			}
		}
	}
	var prodToks []*seedTok
	for _, t := range toks {
		if !t.keep {
			continue
		}
		if _, isAlias := aliases[t.tok]; isAlias {
			continue // alias rule covers it
		}
		for _, r := range t.res {
			t.net += r.net
		}
		prodToks = append(prodToks, t)
	}
	sort.Slice(prodToks, func(i, j int) bool {
		if prodToks[i].net != prodToks[j].net {
			return prodToks[i].net > prodToks[j].net
		}
		return prodToks[i].tok < prodToks[j].tok
	})
	if len(prodToks) > seedMaxProduct {
		prodToks = prodToks[:seedMaxProduct]
	}

	var ws []*seedWRule
	mk := func(prio int, m Match, a Action, auto bool, pre []string, explain string) {
		ws = append(ws, &seedWRule{Rule: Rule{Priority: prio, Match: m, Action: a, Enabled: true, Source: SourceSeed}, auto: auto, pre: pre, explain: explain, src: -1})
	}

	// (5a) shared tokens: highest name-tier precedence.
	sh := map[string]bool{}
	for _, s := range opts.SharedTokens {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			sh[s] = true
		}
	}
	shl := seedSortedKeys(sh)
	for i, s := range shl {
		var hit []*seedRes
		for _, ra := range resList {
			if strings.Contains(ra.name, s) {
				hit = append(hit, ra)
			}
		}
		ex := fmt.Sprintf("shared token %q requested", s)
		if len(hit) > 0 {
			ex += fmt.Sprintf("; matches %d resources: %s", len(hit), seedExamples(hit))
		}
		mk(min(TierNameMin+i, TierNameMin+19), Match{FieldResourceName, OpContains, "", s}, Action{Shared: true}, false, nil, ex)
	}

	// (1) label rules.
	var lvs []lv
	for x := range labelRows {
		lvs = append(lvs, x)
	}
	sort.Slice(lvs, func(i, j int) bool {
		if lvs[i].key != lvs[j].key {
			return lvs[i].key < lvs[j].key
		}
		return lvs[i].value < lvs[j].value
	})
	nLabel := 0
	for _, x := range lvs {
		if nLabel >= seedMaxLabel {
			break
		}
		var act Action
		_, aliased := aliases[x.value]
		switch {
		case aliased:
			act.Product = aliases[x.value]
		case x.value == SharedLabelValue:
			if x.key == "product" {
				continue // built-in
			}
			act.Shared = true
		default:
			p, ok := seedSanitizeName(x.value)
			if !ok || (x.key == "product" && p == x.value) {
				continue // invalid, or identical to the built-in product label
			}
			act.Product = p
		}
		var rs []*seedRes
		for _, r := range labelRes[x] {
			rs = append(rs, r)
		}
		ex := fmt.Sprintf("label %s=%q seen on %d rows", x.key, x.value, labelRows[x])
		if len(rs) > 0 {
			ex = fmt.Sprintf("label %s=%q appears on %d resources: %s", x.key, x.value, len(rs), seedExamples(rs))
		}
		mk(TierLabelMin+nLabel, Match{FieldLabel, OpEquals, x.key, x.value}, act, !aliased, nil, ex)
		nLabel++
	}

	// (5b) name aliases.
	akeys := seedSortedKeys(aliases)
	nAlias := 0
	for _, k := range akeys {
		var hit []*seedRes
		for _, ra := range resList {
			if ra.uni[k] || ra.bi[k] || strings.Contains(ra.name, k) && len(k) >= 3 {
				hit = append(hit, ra)
			}
		}
		if len(hit) == 0 || len(k) < 2 || nAlias >= seedMaxAlias {
			continue
		}
		re, pre := seedTokenRegex(k)
		if strings.ContainsAny(k, "-_. ") == false && seedNameRe.MatchString(k) {
			// plain token
		} else {
			re, pre = regexp.QuoteMeta(k), k
		}
		mk(TierNameMin+20+nAlias, Match{FieldResourceName, OpRegex, "", re}, Action{Product: aliases[k]}, false, []string{pre},
			fmt.Sprintf("alias %q → %s appears on %d resources: %s", k, aliases[k], len(hit), seedExamples(hit)))
		nAlias++
	}

	// (3) product tokens.
	for i, t := range prodToks {
		re, pre := seedTokenRegex(t.tok)
		mk(TierNameMin+100+i, Match{FieldResourceName, OpRegex, "", re}, Action{Product: t.tok}, true, []string{pre},
			fmt.Sprintf("token %q appears on %d resources: %s", t.tok, len(t.res), seedExamples(t.res)))
	}
	// environment tokens.
	envRes := map[string][]*seedRes{}
	for _, ra := range resList {
		for e := range ra.envs {
			envRes[e] = append(envRes[e], ra)
		}
	}
	for i, env := range seedEnvOrder {
		rs := envRes[env]
		t := &seedTok{res: rs}
		for _, r := range rs {
			t.net += r.net
		}
		if !qualifies(t) {
			continue
		}
		syn := seedEnvSynonyms[env]
		mk(TierNameMin+200+i, Match{FieldResourceName, OpRegex, "", `(^|[^a-z])(` + strings.Join(syn, "|") + `)($|[^a-z])`},
			Action{Environment: env}, true, syn,
			fmt.Sprintf("environment token %q appears on %d resources: %s", strings.Join(syn, "/"), len(rs), seedExamples(rs)))
	}

	// (4) SKU / service shared fallbacks (tier 900-999).
	if sharedSKU.fwd > 0 {
		mk(TierSharedMin, Match{FieldSKU, OpContains, "", "forwarding rule"}, Action{Shared: true}, true, nil,
			fmt.Sprintf("load-balancer forwarding-rule SKU seen on %d rows", sharedSKU.fwd))
	}
	if sharedSKU.vmm > 0 {
		mk(TierSharedMin+10, Match{FieldService, OpEquals, "", "VM Manager"}, Action{Shared: true}, true, nil,
			fmt.Sprintf("VM Manager service seen on %d rows", sharedSKU.vmm))
	}
	if sharedSKU.ar > 0 {
		mk(TierSharedMin+20, Match{FieldService, OpEquals, "", "Artifact Registry"}, Action{Shared: true}, true, nil,
			fmt.Sprintf("Artifact Registry storage seen on %d rows", sharedSKU.ar))
	}
	if sharedSKU.minimum > 0 {
		mk(TierSharedMin+30, Match{FieldSKU, OpContains, "", "minimum"}, Action{Shared: true}, true, nil,
			fmt.Sprintf("Networking minimum charge seen on %d rows", sharedSKU.minimum))
	}

	// Existing rules: kept non-seed rules take part in evaluation; seed replaced.
	var kept []*seedWRule
	keptKeys := map[string]bool{}
	counts := SeedCounts{}
	var before []*seedWRule
	for _, r := range opts.Existing {
		before = append(before, &seedWRule{Rule: r, src: -1})
		if r.Source == SourceSeed {
			counts.ReplacedSeedRules++
			continue
		}
		counts.KeptUserRules++
		kept = append(kept, &seedWRule{Rule: r, src: -1})
		if r.Enabled {
			keptKeys[seedRuleKey(r)] = true
		}
	}
	var props []*seedWRule
	for _, w := range ws {
		if keptKeys[seedRuleKey(w.Rule)] {
			continue
		}
		props = append(props, w)
	}

	prepped := make([]seedPrepRow, len(rows))
	for i, r := range rows {
		prepped[i] = seedPrep(r)
	}
	build := func(ps []*seedWRule) *seedEvaluator {
		all := append(append([]*seedWRule(nil), kept...), ps...)
		return newSeedEvaluator(all)
	}

	// Prune auto rules that decide nothing (they are redundant noise).
	ev := build(props)
	vs := seedEvalAll(ev, prepped)
	used := seedDecided(ev, vs, prepped)
	var pruned []*seedWRule
	for _, w := range props {
		if w.auto && used[w] == nil {
			continue
		}
		pruned = append(pruned, w)
	}
	props = pruned

	// (2) project rules.
	ev = build(props)
	vs = seedEvalAll(ev, prepped)
	type pagg struct {
		net, attrNet                float64
		prods                       map[string]bool
		res                         map[string]bool
		shared, unresolved, anyRows bool
		resNames                    []*seedRes
	}
	pm := map[string]*pagg{}
	for i, r := range rows {
		pa := pm[prepped[i].project]
		if pa == nil {
			pa = &pagg{prods: map[string]bool{}, res: map[string]bool{}}
			pm[prepped[i].project] = pa
		}
		if r.Net > 0 {
			pa.net += r.Net
		}
		switch {
		case vs[i].shared:
			pa.shared = true
		case vs[i].product != "":
			pa.prods[vs[i].product] = true
			if r.Net > 0 {
				pa.attrNet += r.Net
			}
			if prepped[i].name != "" {
				pa.res[prepped[i].name] = true
				if ra := resMap[prepped[i].project+"\x00"+prepped[i].name]; ra != nil {
					pa.resNames = append(pa.resNames, ra)
				}
			}
		default:
			pa.unresolved = true
		}
	}
	var pids []string
	for p, pa := range pm {
		if p != "" && len(pa.prods) == 1 && !pa.shared && pa.unresolved && len(pa.res) >= 2 && pa.attrNet >= 0.5*pa.net {
			pids = append(pids, p)
		}
	}
	sort.Slice(pids, func(i, j int) bool {
		if pm[pids[i]].net != pm[pids[j]].net {
			return pm[pids[i]].net > pm[pids[j]].net
		}
		return pids[i] < pids[j]
	})
	if len(pids) > seedMaxProject {
		pids = pids[:seedMaxProject]
	}
	for i, p := range pids {
		pa := pm[p]
		prod := seedSortedKeys(pa.prods)[0]
		seen := map[string]bool{}
		var uniq []*seedRes
		for _, ra := range pa.resNames {
			if !seen[ra.key] {
				seen[ra.key] = true
				uniq = append(uniq, ra)
			}
		}
		props = append(props, &seedWRule{Rule: Rule{Priority: TierProjectMin + i, Match: Match{FieldProject, OpEquals, "", p},
			Action: Action{Product: prod}, Enabled: true, Source: SourceSeed}, src: -1,
			explain: fmt.Sprintf("project %q: all %d attributed resources belong to %s: %s", p, len(uniq), prod, seedExamples(uniq))})
	}

	// Final ordering, statistics, coverage.
	sort.SliceStable(props, func(i, j int) bool {
		a, b := props[i], props[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return seedMatchText(a.Rule) < seedMatchText(b.Rule)
	})
	for i, w := range props {
		w.src = i
	}
	ev = build(props)
	vs = seedEvalAll(ev, prepped)
	used = seedDecided(ev, vs, prepped)

	res := SeedResult{Proposed: make([]Rule, 0, len(props)), Products: []ProductSummary{}}
	stats := make([]SeedRuleStat, 0, len(props))
	prodSet, envSet := map[string]bool{}, map[string]bool{}
	for _, w := range props {
		st := SeedRuleStat{Explanation: w.explain}
		if u := used[w]; u != nil {
			st.Rows, st.Net, st.Resources = u.rows, u.net, len(u.res)
		}
		note := fmt.Sprintf("%s — %d rows, %d resources, %.2f net", w.explain, st.Rows, st.Resources, st.Net)
		r := w.Rule
		r.ID = nil
		r.Note = &note
		res.Proposed = append(res.Proposed, r)
		stats = append(stats, st)
		switch {
		case r.Priority <= TierLabelMax:
			counts.Label++
		case r.Priority <= TierProjectMax:
			counts.Project++
		case r.Priority <= TierNameMax:
			counts.ResourceName++
		default:
			counts.SKUService++
		}
		if r.Action.Product != "" {
			prodSet[r.Action.Product] = true
		}
		if r.Action.Environment != "" {
			envSet[r.Action.Environment] = true
		}
	}
	counts.Rules, counts.Products, counts.Environments = len(props), len(prodSet), len(envSet)

	// Product summaries from the final verdicts.
	type psum struct {
		net float64
		res map[string]bool
	}
	psm := map[string]*psum{}
	umap := map[string]*UnmatchedResource{}
	for i, r := range rows {
		v := vs[i]
		if v.product != "" && !v.shared {
			s := psm[v.product]
			if s == nil {
				s = &psum{res: map[string]bool{}}
				psm[v.product] = s
			}
			s.net += r.Net
			if prepped[i].name != "" {
				s.res[prepped[i].project+"\x00"+prepped[i].name] = true
			}
			continue
		}
		if v.shared {
			continue
		}
		k := prepped[i].project + "\x00" + prepped[i].name
		if prepped[i].name == "" {
			k += "\x00" + r.Service + "\x00" + r.SKU
		}
		u := umap[k]
		if u == nil {
			u = &UnmatchedResource{ProjectID: r.ProjectID, ResourceName: r.ResourceName, Service: r.Service, SKU: r.SKU}
			umap[k] = u
		}
		u.Net += r.Net
		if prepped[i].name != "" && u.Service != r.Service {
			u.Service = "" // spans several services
		}
		if prepped[i].name != "" {
			u.SKU = ""
		}
	}
	for p, s := range psm {
		res.Products = append(res.Products, ProductSummary{Product: p, Net: s.net, Resources: len(s.res)})
	}
	sort.Slice(res.Products, func(i, j int) bool {
		if res.Products[i].Net != res.Products[j].Net {
			return res.Products[i].Net > res.Products[j].Net
		}
		return res.Products[i].Product < res.Products[j].Product
	})
	um := make([]UnmatchedResource, 0, len(umap))
	for _, u := range umap {
		um = append(um, *u)
	}
	sort.Slice(um, func(i, j int) bool {
		if um[i].Net != um[j].Net {
			return um[i].Net > um[j].Net
		}
		a, b := um[i], um[j]
		return a.ProjectID+"\x00"+a.ResourceName+"\x00"+a.Service+"\x00"+a.SKU < b.ProjectID+"\x00"+b.ResourceName+"\x00"+b.Service+"\x00"+b.SKU
	})
	if len(um) > seedTopUnmatched {
		um = um[:seedTopUnmatched]
	}
	res.Counts = counts

	bev := newSeedEvaluator(before)
	return SeedAnalysis{
		Result:         res,
		CoverageBefore: seedCoverageOf(prepped, seedEvalAll(bev, prepped)),
		CoverageAfter:  seedCoverageOf(prepped, vs),
		RuleStats:      stats,
		Unmatched:      um,
	}, nil
}

type seedUsage struct {
	rows int
	net  float64
	res  map[string]bool
}

// seedDecided counts, per rule, the rows it decides (product/shared or environment).
func seedDecided(ev *seedEvaluator, vs []seedVerdict, ps []seedPrepRow) map[*seedWRule]*seedUsage {
	out := map[*seedWRule]*seedUsage{}
	hit := func(idx int, p *seedPrepRow) {
		if idx < 0 {
			return
		}
		w := ev.rules[idx]
		u := out[w]
		if u == nil {
			u = &seedUsage{res: map[string]bool{}}
			out[w] = u
		}
		u.rows++
		u.net += p.net
		if p.name != "" {
			u.res[p.project+"\x00"+p.name] = true
		}
	}
	for i := range vs {
		hit(vs[i].prodRule, &ps[i])
		if vs[i].envRule != vs[i].prodRule || vs[i].envRule < 0 {
			hit(vs[i].envRule, &ps[i])
		}
	}
	return out
}

func seedMatchText(r Rule) string {
	return r.Match.Field + "|" + r.Match.Op + "|" + r.Match.Key + "|" + r.Match.Value + "|" + r.Action.Product + "|" + r.Action.Environment
}

func seedAddTok(m map[string]*seedTok, t string, ra *seedRes, bi bool) {
	k := t
	if bi {
		k = "\x00" + t // never collide a bigram with a unigram of the same text
	}
	a := m[k]
	if a == nil {
		a = &seedTok{tok: t, bi: bi}
		m[k] = a
	}
	a.res = append(a.res, ra)
}

func seedSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
