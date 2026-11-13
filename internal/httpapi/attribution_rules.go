package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/smallTechOrg/cloud0/internal/attribution"
	"github.com/smallTechOrg/cloud0/internal/costtable"
	"github.com/smallTechOrg/cloud0/internal/isoweek"
	"github.com/smallTechOrg/cloud0/internal/store"
)

// maxAttrRulesBody is the body cap of the rules/seed/explain/propose endpoints.
const maxAttrRulesBody = 256 << 10

const (
	attrChangeKind    = "attribution"
	attrDefaultLimit  = 100
	attrMaxLimit      = 1000
	attrDefaultLabels = 200
)

// registerAttributionRules wires rules, seed, unattributed, explain, label
// suggestions and propose (spec/api.md → Attribution additions).
func (d Deps) registerAttributionRules(r *router) {
	r.handle(http.MethodGet, apiPrefix+"/attribution/rules", d.handleAttrGetRules)
	r.handle(http.MethodPut, apiPrefix+"/attribution/rules", d.handleAttrPutRules)
	r.handle(http.MethodPost, apiPrefix+"/attribution/seed/preview", d.handleAttrSeedPreview)
	r.handle(http.MethodPost, apiPrefix+"/attribution/seed/apply", d.handleAttrSeedApply)
	r.handle(http.MethodGet, apiPrefix+"/attribution/unattributed", d.handleAttrUnattributed)
	r.handle(http.MethodPost, apiPrefix+"/attribution/explain", d.handleAttrExplain)
	r.handle(http.MethodGet, apiPrefix+"/attribution/label-suggestions", d.handleAttrLabelSuggestions)
	r.handle(http.MethodPost, apiPrefix+"/attribution/propose", d.handleAttrPropose)
}

// ---- errors -----------------------------------------------------------------

func errAttrNotConfigured() *APIError {
	return (&APIError{Status: http.StatusServiceUnavailable, Code: "attribution_not_configured",
		Message: "Attribution is not configured on this server."}).
		WithHint("Restart cloud0 with the attribution engine wired (this is a build/wiring problem, not a setting).")
}

func errAttrInvalidRule(msg string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: attribution.CodeInvalidRule, Message: msg}).
		WithHint("Fix the rule at the index named in the message; nothing was saved.")
}

func errAttrInvalidAllocate(v string) *APIError {
	return (&APIError{Status: http.StatusBadRequest, Code: attribution.CodeInvalidAllocate,
		Message: "The allocate mode " + quote(v) + " is not valid."}).
		WithHint("Use one of none, even, proportional.")
}

func errAttrProposerNotConfigured() *APIError {
	return (&APIError{Status: http.StatusServiceUnavailable, Code: attribution.CodeProposerNotConfigured,
		Message: "The LLM rule proposer is not configured."}).
		WithHint("Set CLOUD0_OPENROUTER_API_KEY in .env and restart to enable LLM rule proposals.")
}

func errAttrProposerFailed(err error) *APIError {
	msg := strings.TrimSpace(err.Error())
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return (&APIError{Status: http.StatusBadGateway, Code: attribution.CodeProposerFailed,
		Message: "The rule proposer failed; no rules were changed.", Cause: err}).WithHint(msg)
}

// attrDeps returns the wired deps or the 503 envelope error.
func (d Deps) attrDeps() (*AttributionDeps, *APIError) {
	if d.Attribution == nil || d.Attribution.NewEngine == nil {
		return nil, errAttrNotConfigured()
	}
	return d.Attribution, nil
}

// ---- body decoding ----------------------------------------------------------

// attrDecode reads one JSON object (strict, 256 KiB). An empty body decodes to
// the zero value when allowEmpty.
func attrDecode(w http.ResponseWriter, r *http.Request, v any, allowEmpty bool) *APIError {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAttrRulesBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) && allowEmpty {
			return nil
		}
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return ErrInvalidRequest("The request body is larger than 256 KiB.").WithHint("Send fewer rules per request.")
		}
		return ErrInvalidRequest("The request body is not valid JSON for this endpoint.").
			WithHint("Send a single JSON object with the documented fields and no unknown fields (see spec/api.md).")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ErrInvalidRequest("The request body must contain exactly one JSON object.")
	}
	return nil
}

// ---- rule conversion --------------------------------------------------------

func attrStoreToRule(sr store.AttributionRule) attribution.Rule {
	id := sr.ID
	return attribution.Rule{
		ID: &id, Priority: sr.Priority,
		Match:   attribution.Match{Field: sr.MatchField, Op: sr.MatchOp, Key: sr.MatchKey, Value: sr.MatchValue},
		Action:  attribution.Action{Product: sr.Product, Environment: sr.Environment, Shared: sr.Shared},
		Enabled: sr.Enabled, Source: sr.Source, Note: sr.Note,
	}
}

func attrRuleToStore(r attribution.Rule) store.AttributionRule {
	sr := store.AttributionRule{
		Priority: r.Priority, MatchField: r.Match.Field, MatchOp: r.Match.Op, MatchKey: r.Match.Key, MatchValue: r.Match.Value,
		Product: r.Action.Product, Environment: r.Action.Environment, Shared: r.Action.Shared,
		Enabled: r.Enabled, Source: r.Source, Note: r.Note,
	}
	if r.ID != nil && *r.ID > 0 {
		sr.ID = *r.ID
	}
	return sr
}

func attrStoreToRules(in []store.AttributionRule) []attribution.Rule {
	out := make([]attribution.Rule, 0, len(in))
	for _, sr := range in {
		out = append(out, attrStoreToRule(sr))
	}
	return out
}

// attrRuleIn is a rule as received in a PUT: enabled defaults to true and
// source to "user" when omitted.
type attrRuleIn struct {
	ID       *int64             `json:"id"`
	Priority int                `json:"priority"`
	Match    attribution.Match  `json:"match"`
	Action   attribution.Action `json:"action"`
	Enabled  *bool              `json:"enabled"`
	Source   string             `json:"source"`
	Note     *string            `json:"note"`
}

func (in attrRuleIn) rule() attribution.Rule {
	r := attribution.Rule{ID: in.ID, Priority: in.Priority, Match: in.Match, Action: in.Action, Enabled: true, Source: in.Source, Note: in.Note}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	if r.Source == "" {
		r.Source = attribution.SourceUser
	}
	r.Action.Product = attrNorm(r.Action.Product)
	r.Action.Environment = attrNorm(r.Action.Environment)
	if r.ID != nil && *r.ID <= 0 {
		r.ID = nil
	}
	return r
}

func attrNorm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// attrNormAliases lower-cases/trims and validates an alias → product map.
func attrNormAliases(in map[string]string) (map[string]string, *APIError) {
	out := make(map[string]string, len(in))
	for k, v := range in {
		k, v = attrNorm(k), attrNorm(v)
		if k == "" || !store.ValidProductName(v) {
			return nil, ErrInvalidRequest("The alias " + quote(k) + " → " + quote(v) + " is not valid.").
				WithHint("Aliases map a non-empty alias to a product name matching ^[a-z0-9][a-z0-9_-]{0,39}$.")
		}
		out[k] = v
	}
	return out, nil
}

// ---- stored state -----------------------------------------------------------

func (d Deps) attrAliases(ctx context.Context, q store.Querier) (map[string]string, error) {
	kv, err := store.GetSettingsWithPrefix(ctx, q, store.KeyAttributionAliases)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if raw, ok := kv[store.KeyAttributionAliases]; ok {
		if json.Unmarshal([]byte(raw), &out) != nil || out == nil {
			out = map[string]string{}
		}
	}
	return out, nil
}

func (d Deps) attrAllocate(ctx context.Context, q store.Querier) (attribution.AllocateMode, error) {
	kv, err := store.GetSettingsWithPrefix(ctx, q, store.KeyAttributionAllocate)
	if err != nil {
		return "", err
	}
	if m := attribution.AllocateMode(kv[store.KeyAttributionAllocate]); m.Valid() {
		return m, nil
	}
	return attribution.AllocateNone, nil
}

func (d Deps) attrProposerInfo(ad *AttributionDeps) attribution.ProposerInfo {
	if ad == nil || ad.Proposer == nil || !ad.Proposer.Enabled() {
		return attribution.ProposerInfo{}
	}
	info := attribution.ProposerInfo{Enabled: true}
	if m := ad.Proposer.Model(); m != "" {
		info.Model = &m
	}
	return info
}

func (d Deps) attrRulesBody(ctx context.Context, q store.Querier, ad *AttributionDeps) (attribution.RulesResponse, error) {
	stored, err := store.ListAttributionRules(ctx, q)
	if err != nil {
		return attribution.RulesResponse{}, err
	}
	aliases, err := d.attrAliases(ctx, q)
	if err != nil {
		return attribution.RulesResponse{}, err
	}
	alloc, err := d.attrAllocate(ctx, q)
	if err != nil {
		return attribution.RulesResponse{}, err
	}
	upd, err := store.AttributionRulesUpdatedAt(ctx, q)
	if err != nil {
		return attribution.RulesResponse{}, err
	}
	return attribution.RulesResponse{Rules: attrStoreToRules(stored), Allocate: alloc, Aliases: aliases,
		Proposer: d.attrProposerInfo(ad), UpdatedAt: upd}, nil
}

// attrChangePayload is the before/after JSON of attribution change_history rows.
type attrChangePayload struct {
	Rules    []attribution.Rule       `json:"rules"`
	Allocate attribution.AllocateMode `json:"allocate"`
	Aliases  map[string]string        `json:"aliases"`
}

func (d Deps) attrSnapshotState(ctx context.Context, q store.Querier) (json.RawMessage, error) {
	stored, err := store.ListAttributionRules(ctx, q)
	if err != nil {
		return nil, err
	}
	aliases, err := d.attrAliases(ctx, q)
	if err != nil {
		return nil, err
	}
	alloc, err := d.attrAllocate(ctx, q)
	if err != nil {
		return nil, err
	}
	return json.Marshal(attrChangePayload{Rules: attrStoreToRules(stored), Allocate: alloc, Aliases: aliases})
}

// attrContentKey is a rule's content without id, used to detect no-op writes.
func attrRulesContent(rs []attribution.Rule) string {
	cp := make([]attribution.Rule, len(rs))
	for i, r := range rs {
		r.ID = nil
		cp[i] = r
	}
	b, _ := json.Marshal(cp)
	return string(b)
}

// ---- GET / PUT rules --------------------------------------------------------

func (d Deps) handleAttrGetRules(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	body, err := d.attrRulesBody(r.Context(), d.Store.DB, ad)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

func (d Deps) handleAttrPutRules(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	var in struct {
		Rules    *[]attrRuleIn     `json:"rules"`
		Allocate *string           `json:"allocate"`
		Aliases  map[string]string `json:"aliases"`
	}
	if aerr := attrDecode(w, r, &in, false); aerr != nil {
		return aerr
	}
	if in.Rules == nil {
		return ErrInvalidRequest(`The body must contain a "rules" array.`).WithHint(`Send {"rules": [...]}; an empty array clears all rules.`)
	}
	var alloc *attribution.AllocateMode
	if in.Allocate != nil {
		m := attribution.AllocateMode(*in.Allocate)
		if !m.Valid() {
			return errAttrInvalidAllocate(*in.Allocate)
		}
		alloc = &m
	}
	var aliases map[string]string
	if in.Aliases != nil {
		if aliases, aerr = attrNormAliases(in.Aliases); aerr != nil {
			return aerr
		}
	}
	if len(*in.Rules) > attribution.MaxRules {
		return errAttrInvalidRule("Too many rules: " + strconv.Itoa(len(*in.Rules)) + " exceeds the limit of " + strconv.Itoa(attribution.MaxRules) + ".")
	}
	rules := make([]attribution.Rule, 0, len(*in.Rules))
	for _, ri := range *in.Rules {
		rules = append(rules, ri.rule())
	}
	// Validate through the engine factory first: nothing is written on failure.
	if _, err := ad.NewEngine(rules); err != nil {
		if errors.Is(err, attribution.ErrInvalidRule) {
			return errAttrInvalidRule(err.Error())
		}
		return ErrInternal(err)
	}
	ctx := r.Context()
	now := d.now()
	err := d.Store.WithTx(ctx, func(tx *sql.Tx) error {
		before, err := d.attrSnapshotState(ctx, tx)
		if err != nil {
			return err
		}
		sr := make([]store.AttributionRule, len(rules))
		for i, ru := range rules {
			sr[i] = attrRuleToStore(ru)
		}
		if _, err := store.ReplaceAttributionRules(ctx, tx, sr, now); err != nil {
			return err
		}
		kv := map[string]string{}
		if alloc != nil {
			kv[store.KeyAttributionAllocate] = string(*alloc)
		}
		if aliases != nil {
			b, _ := json.Marshal(aliases)
			kv[store.KeyAttributionAliases] = string(b)
		}
		if len(kv) > 0 {
			if err := store.PutSettings(ctx, tx, kv, now); err != nil {
				return err
			}
		}
		return d.attrRecordChange(ctx, tx, "update", "rules", before)
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			return errAttrInvalidRule(err.Error())
		}
		return ErrInternal(err)
	}
	body, err := d.attrRulesBody(ctx, d.Store.DB, ad)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

// attrRecordChange appends a change_history row when the attribution state
// differs (ignoring rule ids and timestamps) from before. Runs in the caller's tx.
func (d Deps) attrRecordChange(ctx context.Context, tx *sql.Tx, action, target string, before json.RawMessage) error {
	after, err := d.attrSnapshotState(ctx, tx)
	if err != nil {
		return err
	}
	var b, a attrChangePayload
	_ = json.Unmarshal(before, &b)
	_ = json.Unmarshal(after, &a)
	if attrRulesContent(b.Rules) == attrRulesContent(a.Rules) && b.Allocate == a.Allocate && attrAliasesEqual(b.Aliases, a.Aliases) {
		return nil
	}
	_, err = store.InsertChange(ctx, tx, store.NewChange{At: d.now(), Kind: attrChangeKind, Action: action, Target: target, Before: before, After: after})
	return err
}

func attrAliasesEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// ---- snapshot loading -------------------------------------------------------

type attrCtx struct {
	week     isoweek.Week
	scope    string
	snap     *store.Snapshot
	cost     []attribution.CostRow
	labels   bool
	stored   []store.AttributionRule
	rules    []attribution.Rule
	eng      attribution.Engine
	current  isoweek.Status
	snapshot *attribution.SnapshotRef
}

func attrParseLabels(raw *string) map[string]string {
	out := map[string]string{}
	if raw == nil || *raw == "" {
		return out
	}
	var kv []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if json.Unmarshal([]byte(*raw), &kv) != nil {
		return out
	}
	for _, e := range kv {
		if e.Key != "" {
			out[e.Key] = e.Value
		}
	}
	return out
}

func attrDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// attrLoad resolves week/scope, loads the latest snapshot of the week with its
// rows (labels parsed), the stored rules and an engine over them. snap is nil
// when the week has no snapshot.
func (d Deps) attrLoad(ctx context.Context, ad *AttributionDeps, weekStr, scopeStr string) (*attrCtx, *APIError) {
	now := d.now()
	week, aerr := d.weekParam(weekStr, now)
	if aerr != nil {
		return nil, aerr
	}
	scope, aerr := d.scopeParam(scopeStr)
	if aerr != nil {
		return nil, aerr
	}
	ac := &attrCtx{week: week, scope: scope, current: isoweek.StatusAt(week, now, d.settle())}
	if ac.stored, aerr = d.attrStored(ctx); aerr != nil {
		return nil, aerr
	}
	ac.rules = attrStoreToRules(ac.stored)
	eng, err := ad.NewEngine(ac.rules)
	if err != nil {
		return nil, ErrInternal(err)
	}
	ac.eng = eng
	latest, err := store.LatestPerWeek(ctx, d.Store.DB, week, week)
	if err != nil {
		return nil, ErrInternal(err)
	}
	snap, ok := latest[week]
	if !ok {
		return ac, nil
	}
	ac.snap = &snap
	rows, err := store.SnapshotRows(ctx, d.Store.DB, snap.ID, scope)
	if err != nil {
		return nil, ErrInternal(err)
	}
	ac.labels = len(rows) == 0
	ac.cost = make([]attribution.CostRow, 0, len(rows))
	for _, sr := range rows {
		if sr.LabelsJSON != nil {
			ac.labels = true
		}
		ac.cost = append(ac.cost, attribution.CostRow{
			Row: attribution.Row{ProjectID: sr.ProjectID, Service: sr.Service, SKU: sr.SKU,
				ResourceName: attrDeref(sr.ResourceName), ResourceGlobalName: attrDeref(sr.ResourceGlobalName),
				Labels: attrParseLabels(sr.LabelsJSON)},
			Gross: sr.Gross, Credits: sr.Credits, Net: sr.Net, LineItems: sr.LineItems,
		})
	}
	ref := attribution.SnapshotRef{ID: snap.ID, FetchedAt: snap.FetchedAt.UTC(), StatusAtFetch: string(snap.StatusAtFetch),
		RunID: snap.RunID, RowCount: snap.RowCount, Stale: costtable.Stale(snap.StatusAtFetch, ac.current)}
	ac.snapshot = &ref
	return ac, nil
}

func (d Deps) attrStored(ctx context.Context) ([]store.AttributionRule, *APIError) {
	stored, err := store.ListAttributionRules(ctx, d.Store.DB)
	if err != nil {
		return nil, ErrInternal(err)
	}
	return stored, nil
}

// ---- coverage ---------------------------------------------------------------

func attrR2(v float64) float64 { return math.Round(v*100) / 100 }
func attrR1(v float64) float64 { return math.Round(v*10) / 10 }

func attrPct(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return attrR1(part / total * 100)
}

// attrCoverage classifies every row with eng: shared, attributed (a product)
// or unattributed.
func attrCoverage(rows []attribution.CostRow, eng attribution.Engine) attribution.Coverage {
	var total, attributed, shared, un float64
	for _, cr := range rows {
		ev := eng.Evaluate(cr.Row)
		total += cr.Net
		switch {
		case ev.Shared:
			shared += cr.Net
		case ev.Product != "":
			attributed += cr.Net
		default:
			un += cr.Net
		}
	}
	return attribution.Coverage{TotalNet: attrR2(total), AttributedNet: attrR2(attributed), SharedNet: attrR2(shared), UnattributedNet: attrR2(un),
		AttributedPct: attrPct(attributed, total), SharedPct: attrPct(shared, total), UnattributedPct: attrPct(un, total)}
}

// ---- seed -------------------------------------------------------------------

type attrSeedBody struct {
	Week         string            `json:"week"`
	Scope        string            `json:"scope"`
	Aliases      map[string]string `json:"aliases"`
	SharedTokens []string          `json:"shared_tokens"`
}

type attrSeedPlan struct {
	ac       *attrCtx
	aliases  map[string]string
	userSet  bool // body supplied aliases
	result   attribution.SeedResult
	merged   []attribution.Rule // kept non-seed rules + proposed
	coverage attribution.Coverage
	before   attribution.Coverage
	warnings []string
}

const attrNoSnapshotWarning = "No snapshot for this week — Refresh first."

func (d Deps) attrSeedPlan(w http.ResponseWriter, r *http.Request) (*AttributionDeps, *attrSeedPlan, *APIError) {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return nil, nil, aerr
	}
	if ad.Seeder == nil {
		return nil, nil, errAttrNotConfigured()
	}
	var in attrSeedBody
	if aerr := attrDecode(w, r, &in, true); aerr != nil {
		return nil, nil, aerr
	}
	ctx := r.Context()
	ac, aerr := d.attrLoad(ctx, ad, in.Week, in.Scope)
	if aerr != nil {
		return nil, nil, aerr
	}
	plan := &attrSeedPlan{ac: ac, warnings: []string{}}
	stored, err := d.attrAliases(ctx, d.Store.DB)
	if err != nil {
		return nil, nil, ErrInternal(err)
	}
	plan.aliases = stored
	if in.Aliases != nil {
		if plan.aliases, aerr = attrNormAliases(in.Aliases); aerr != nil {
			return nil, nil, aerr
		}
		plan.userSet = true
	}
	tokens := make([]string, 0, len(in.SharedTokens))
	for _, t := range in.SharedTokens {
		if t = strings.TrimSpace(t); t != "" {
			tokens = append(tokens, t)
		}
	}
	if ac.snap == nil {
		plan.warnings = append(plan.warnings, attrNoSnapshotWarning)
		plan.result = attribution.SeedResult{Proposed: []attribution.Rule{}, Products: []attribution.ProductSummary{}}
		return ad, plan, nil
	}
	if !ac.labels {
		plan.warnings = append(plan.warnings, "Labels not captured for this week; refresh it to enable label rules.")
	}
	res, err := ad.Seeder.Seed(ac.cost, attribution.SeedOptions{Aliases: plan.aliases, SharedTokens: tokens, Existing: ac.rules})
	if err != nil {
		return nil, nil, ErrInternal(err)
	}
	if res.Proposed == nil {
		res.Proposed = []attribution.Rule{}
	}
	if res.Products == nil {
		res.Products = []attribution.ProductSummary{}
	}
	for i := range res.Proposed {
		res.Proposed[i].ID = nil
		if res.Proposed[i].Source == "" {
			res.Proposed[i].Source = attribution.SourceSeed
		}
	}
	plan.result = res
	for _, ru := range ac.rules {
		if ru.Source != attribution.SourceSeed {
			plan.merged = append(plan.merged, ru)
		}
	}
	plan.merged = append(plan.merged, res.Proposed...)
	eng, err := ad.NewEngine(plan.merged)
	if err != nil {
		return nil, nil, ErrInternal(err)
	}
	plan.coverage = attrCoverage(ac.cost, eng)
	plan.before = attrCoverage(ac.cost, ac.eng)
	return ad, plan, nil
}

func (d Deps) handleAttrSeedPreview(w http.ResponseWriter, r *http.Request) *APIError {
	_, p, aerr := d.attrSeedPlan(w, r)
	if aerr != nil {
		return aerr
	}
	writeJSON(w, http.StatusOK, attribution.SeedPreview{
		Week: p.ac.week.String(), Snapshot: p.ac.snapshot, LabelsCaptured: p.ac.labels && p.ac.snap != nil,
		Proposed: p.result.Proposed, Counts: p.result.Counts, Coverage: p.coverage, CoverageBefore: p.before,
		Products: p.result.Products, Warnings: p.warnings,
	})
	return nil
}

func (d Deps) handleAttrSeedApply(w http.ResponseWriter, r *http.Request) *APIError {
	ad, p, aerr := d.attrSeedPlan(w, r)
	if aerr != nil {
		return aerr
	}
	ctx := r.Context()
	if p.ac.snap == nil {
		writeJSON(w, http.StatusOK, attribution.SeedApplied{Applied: false, Rules: p.ac.rules, Warnings: p.warnings})
		return nil
	}
	now := d.now()
	// Reuse the ids of stored seed rules with identical content so applying
	// the same input twice leaves the rule set (ids included) untouched.
	free := map[string][]int64{}
	for _, sr := range p.ac.stored {
		if sr.Source == attribution.SourceSeed {
			k := attrSeedKey(attrStoreToRule(sr))
			free[k] = append(free[k], sr.ID)
		}
	}
	var final []store.AttributionRule
	for _, sr := range p.ac.stored {
		if sr.Source != attribution.SourceSeed {
			final = append(final, sr)
		}
	}
	for _, ru := range p.result.Proposed {
		sr := attrRuleToStore(ru)
		k := attrSeedKey(ru)
		if ids := free[k]; len(ids) > 0 {
			sr.ID, free[k] = ids[0], ids[1:]
		}
		final = append(final, sr)
	}
	var stored []store.AttributionRule
	err := d.Store.WithTx(ctx, func(tx *sql.Tx) error {
		before, err := d.attrSnapshotState(ctx, tx)
		if err != nil {
			return err
		}
		if stored, err = store.ReplaceAttributionRules(ctx, tx, final, now); err != nil {
			return err
		}
		if p.userSet {
			b, _ := json.Marshal(p.aliases)
			if err := store.PutSettings(ctx, tx, map[string]string{store.KeyAttributionAliases: string(b)}, now); err != nil {
				return err
			}
		}
		return d.attrRecordChange(ctx, tx, "seed_apply", "rules", before)
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalidInput) {
			return errAttrInvalidRule(err.Error())
		}
		return ErrInternal(err)
	}
	rules := attrStoreToRules(stored)
	eng, err := ad.NewEngine(rules)
	if err != nil {
		return ErrInternal(err)
	}
	writeJSON(w, http.StatusOK, attribution.SeedApplied{Applied: true, Rules: rules, Counts: p.result.Counts,
		Coverage: attrCoverage(p.ac.cost, eng), Warnings: p.warnings})
	return nil
}

func attrSeedKey(r attribution.Rule) string {
	b, _ := json.Marshal(struct {
		P int
		M attribution.Match
		A attribution.Action
		N *string
	}{r.Priority, r.Match, r.Action, r.Note})
	return string(b)
}

// ---- unattributed -----------------------------------------------------------

type attrGroup struct {
	item     attribution.UnattributedItem
	byServ   map[string]float64
	labels   map[string]string
	bestNet  float64
	bestEval attribution.Evaluation
	hasEval  bool
	rowName  string
	global   string
}

// attrUnattributed groups the rows whose product is unresolved (not shared)
// into items sorted by net desc then key. Suggestions come from the seeder
// when ad.Seeder is wired.
func (d Deps) attrUnattributed(ad *AttributionDeps, ac *attrCtx, suggest bool) (items []attribution.UnattributedItem, unNet, total float64) {
	groups := map[string]*attrGroup{}
	for _, cr := range ac.cost {
		total += cr.Net
		ev := ac.eng.Evaluate(cr.Row)
		if ev.Shared || ev.Product != "" {
			continue
		}
		unNet += cr.Net
		var key string
		resource := cr.ResourceName != ""
		if resource {
			key = "r\x00" + cr.ProjectID + "\x00" + cr.ResourceName
		} else {
			key = "s\x00" + cr.ProjectID + "\x00" + cr.Service + "\x00" + cr.SKU
		}
		g := groups[key]
		if g == nil {
			g = &attrGroup{byServ: map[string]float64{}, labels: map[string]string{}}
			g.item = attribution.UnattributedItem{ProjectID: cr.ProjectID, Service: cr.Service, Evidence: []string{}}
			if resource {
				name := cr.ResourceName
				g.item.Kind, g.item.Key, g.item.ResourceName = "resource", name, &name
			} else {
				sku := cr.SKU
				g.item.Kind, g.item.Key, g.item.SKU = "sku", sku, &sku
			}
			groups[key] = g
		}
		g.item.Net += cr.Net
		g.item.LineItems += cr.LineItems
		g.byServ[cr.Service] += cr.Net
		for k, v := range cr.Labels {
			if _, ok := g.labels[k]; !ok {
				g.labels[k] = v
			}
		}
		if cr.ResourceGlobalName != "" && g.global == "" {
			g.global = cr.ResourceGlobalName
			gn := cr.ResourceGlobalName
			g.item.ResourceGlobalName = &gn
		}
		if !g.hasEval || cr.Net > g.bestNet {
			g.bestNet, g.bestEval, g.hasEval = cr.Net, ev, true
			if resource {
				g.item.Service = cr.Service
			}
		}
	}
	var sugEng attribution.Engine
	var sugRules []attribution.Rule
	if suggest && ad != nil && ad.Seeder != nil && len(groups) > 0 {
		if res, err := ad.Seeder.Seed(ac.cost, attribution.SeedOptions{Aliases: nil, Existing: ac.rules}); err == nil && len(res.Proposed) > 0 {
			sugRules = res.Proposed
			if e, err := ad.NewEngine(sugRules); err == nil {
				sugEng = e
			}
		}
	}
	items = make([]attribution.UnattributedItem, 0, len(groups))
	for _, g := range groups {
		it := g.item
		it.Net = attrR2(it.Net)
		if total > 0 {
			it.SharePct = math.Round(g.item.Net/total*10000) / 100
		}
		switch {
		case it.Kind == "sku":
			it.Reason = attribution.ReasonNoResourceName
			it.Evidence = append(it.Evidence, "billing line has no resource name; add a service/SKU rule or mark it shared")
		case g.bestEval.EnvironmentRuleID != nil:
			it.Reason = attribution.ReasonEnvironmentOnly
			it.Evidence = append(it.Evidence, "an environment rule matched but no product rule did")
		default:
			it.Reason = attribution.ReasonNoMatchingRule
			if _, ok := g.labels[attribution.ProductLabelKey]; !ok {
				it.Evidence = append(it.Evidence, "resource has no label product/env")
			}
			it.Evidence = append(it.Evidence, "no enabled rule matched")
		}
		if sugEng != nil {
			row := attribution.Row{ProjectID: it.ProjectID, Service: it.Service, ResourceName: g.item.Key, ResourceGlobalName: g.global, Labels: g.labels}
			if it.Kind == "sku" {
				row = attribution.Row{ProjectID: it.ProjectID, Service: it.Service, SKU: g.item.Key, Labels: g.labels}
			}
			it.Suggested = attrSuggest(sugEng, sugRules, row)
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Net != items[j].Net {
			return items[i].Net > items[j].Net
		}
		return items[i].Key < items[j].Key
	})
	return items, unNet, total
}

func attrSuggest(eng attribution.Engine, rules []attribution.Rule, row attribution.Row) *attribution.Suggested {
	ev := eng.Evaluate(row)
	if ev.Shared || (ev.Product == "" && ev.Environment == "") {
		return nil
	}
	s := &attribution.Suggested{Product: ev.Product, Confidence: attribution.ConfidenceLow}
	if ev.Environment != "" {
		env := ev.Environment
		s.Environment = &env
	}
	if len(ev.Evidence) > 0 {
		s.Basis = ev.Evidence[0]
	}
	if ev.RuleID != nil && *ev.RuleID < 0 {
		if idx := int(-*ev.RuleID) - 1; idx >= 0 && idx < len(rules) {
			switch p := rules[idx].Priority; {
			case p <= attribution.TierLabelMax:
				s.Confidence = attribution.ConfidenceHigh
			case p <= attribution.TierProjectMax:
				s.Confidence = attribution.ConfidenceMedium
			}
		}
	}
	return s
}

func attrLimit(q string, def int) (int, *APIError) {
	if q == "" {
		return def, nil
	}
	n, err := strconv.Atoi(q)
	if err != nil || n < 1 || n > attrMaxLimit {
		return 0, ErrInvalidRequest("The limit " + quote(q) + " is not valid.").WithHint("Use an integer from 1 to " + strconv.Itoa(attrMaxLimit) + ".")
	}
	return n, nil
}

func (d Deps) handleAttrUnattributed(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	q := r.URL.Query()
	limit, aerr := attrLimit(q.Get("limit"), attrDefaultLimit)
	if aerr != nil {
		return aerr
	}
	ac, aerr := d.attrLoad(r.Context(), ad, q.Get("week"), q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	resp := attribution.UnattributedResponse{Week: ac.week.String(), Scope: ac.scope, Snapshot: ac.snapshot,
		LabelsCaptured: ac.labels && ac.snap != nil, Items: []attribution.UnattributedItem{}}
	if ac.snap != nil {
		items, unNet, total := d.attrUnattributed(ad, ac, true)
		resp.TotalNet, resp.UnattributedNet, resp.UnattributedPct = attrR2(total), attrR2(unNet), attrPct(unNet, total)
		resp.ItemCount = len(items)
		if len(items) > limit {
			items, resp.Truncated = items[:limit], true
		}
		resp.Items = items
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// ---- explain ----------------------------------------------------------------

func (d Deps) handleAttrExplain(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	var in attribution.ExplainRequest
	if aerr := attrDecode(w, r, &in, false); aerr != nil {
		return aerr
	}
	needle := strings.ToLower(strings.TrimSpace(in.Resource))
	if needle == "" {
		return ErrInvalidRequest("The resource is required.").WithHint(`Send {"resource": "<resource name or global name>"}.`)
	}
	ac, aerr := d.attrLoad(r.Context(), ad, in.Week, in.Scope)
	if aerr != nil {
		return aerr
	}
	if ac.snap == nil {
		return ErrNotFound("No snapshot for " + ac.week.String() + ".").WithHint("Refresh the week first.")
	}
	var matched []attribution.CostRow
	for _, cr := range ac.cost {
		if strings.ToLower(cr.ResourceName) == needle || strings.ToLower(cr.ResourceGlobalName) == needle {
			matched = append(matched, cr)
		}
	}
	if len(matched) == 0 {
		globals := map[string]bool{}
		for _, cr := range ac.cost {
			if g := strings.ToLower(cr.ResourceGlobalName); g != "" && strings.HasSuffix(g, needle) {
				globals[g] = true
			}
		}
		if len(globals) == 1 {
			for _, cr := range ac.cost {
				if globals[strings.ToLower(cr.ResourceGlobalName)] {
					matched = append(matched, cr)
				}
			}
		}
	}
	if len(matched) == 0 {
		return ErrNotFound("No billing row in " + ac.week.String() + " matches the resource " + quote(in.Resource) + ".").
			WithHint("Use the resource name or the full global name as shown in the Cost drill-down.")
	}
	type key struct{ project, service string }
	agg := map[key]*attribution.ExplainResult{}
	reps := map[key]attribution.Row{}
	for _, cr := range matched {
		k := key{cr.ProjectID, cr.Service}
		res := agg[k]
		if res == nil {
			res = &attribution.ExplainResult{ProjectID: cr.ProjectID, Service: cr.Service, Labels: map[string]string{}}
			if cr.ResourceName != "" {
				n := cr.ResourceName
				res.ResourceName = &n
			}
			if cr.ResourceGlobalName != "" {
				g := cr.ResourceGlobalName
				res.ResourceGlobalName = &g
			}
			agg[k] = res
			reps[k] = cr.Row
		}
		res.Net += cr.Net
		for lk, lv := range cr.Labels {
			if _, ok := res.Labels[lk]; !ok {
				res.Labels[lk] = lv
			}
		}
	}
	results := make([]attribution.ExplainResult, 0, len(agg))
	for k, res := range agg {
		row := reps[k]
		row.Labels = res.Labels
		res.Net = attrR2(res.Net)
		res.Evaluation = ac.eng.Evaluate(row)
		res.Trail = ac.eng.Explain(row)
		if res.Trail == nil {
			res.Trail = []attribution.TrailStep{}
		}
		if res.Evaluation.Evidence == nil {
			res.Evaluation.Evidence = []string{}
		}
		results = append(results, *res)
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.Net != b.Net {
			return a.Net > b.Net
		}
		if a.ProjectID != b.ProjectID {
			return a.ProjectID < b.ProjectID
		}
		return a.Service < b.Service
	})
	writeJSON(w, http.StatusOK, attribution.ExplainResponse{Week: ac.week.String(), Resource: in.Resource, Matches: len(results), Results: results})
	return nil
}

// ---- label suggestions ------------------------------------------------------

func (d Deps) handleAttrLabelSuggestions(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	if ad.Labels == nil {
		return errAttrNotConfigured()
	}
	q := r.URL.Query()
	limit, aerr := attrLimit(q.Get("limit"), attrDefaultLabels)
	if aerr != nil {
		return aerr
	}
	ac, aerr := d.attrLoad(r.Context(), ad, q.Get("week"), q.Get("scope"))
	if aerr != nil {
		return aerr
	}
	resp := attribution.LabelSuggestionsResponse{Week: ac.week.String(), Scope: ac.scope, Snapshot: ac.snapshot,
		LabelsCaptured: ac.labels && ac.snap != nil, Items: []attribution.LabelSuggestion{}}
	if ac.snap != nil {
		items, done := ad.Labels.Suggest(ac.cost, ac.eng)
		resp.Count, resp.DoneCount = len(items), done
		if len(items) > limit {
			items = items[:limit]
		}
		if items != nil {
			resp.Items = items
		}
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

// ---- propose ----------------------------------------------------------------

func (d Deps) handleAttrPropose(w http.ResponseWriter, r *http.Request) *APIError {
	ad, aerr := d.attrDeps()
	if aerr != nil {
		return aerr
	}
	if ad.Proposer == nil || !ad.Proposer.Enabled() {
		return errAttrProposerNotConfigured()
	}
	var in attribution.ProposeRequest
	if aerr := attrDecode(w, r, &in, true); aerr != nil {
		return aerr
	}
	maxRules := in.MaxRules
	if maxRules == 0 {
		maxRules = 20
	}
	if maxRules < 1 || maxRules > 50 {
		return ErrInvalidRequest("max_rules must be between 1 and 50.")
	}
	ctx := r.Context()
	ac, aerr := d.attrLoad(ctx, ad, in.Week, in.Scope)
	if aerr != nil {
		return aerr
	}
	resp := attribution.ProposeResponse{Week: ac.week.String(), Model: ad.Proposer.Model(), Proposals: []attribution.Proposal{}}
	if ac.snap == nil {
		writeJSON(w, http.StatusOK, resp)
		return nil
	}
	items, _, _ := d.attrUnattributed(ad, ac, false)
	if len(items) == 0 {
		resp.CoverageIfApplied = attrCoverage(ac.cost, ac.eng)
		writeJSON(w, http.StatusOK, resp)
		return nil
	}
	out, err := ad.Proposer.Propose(ctx, attribution.ProposeInput{Items: items, Existing: ac.rules, MaxRules: maxRules})
	if err != nil {
		if errors.Is(err, attribution.ErrProposerNotConfigured) {
			return errAttrProposerNotConfigured()
		}
		return errAttrProposerFailed(err)
	}
	resp.Dropped = out.Dropped
	if out.Model != "" {
		resp.Model = out.Model
	}
	resp.LatencyMS, resp.InputTokens, resp.OutputTokens = out.LatencyMS, out.InputTokens, out.OutputTokens
	combined := append([]attribution.Rule(nil), ac.rules...)
	for _, p := range out.Proposals {
		if len(resp.Proposals) >= maxRules {
			resp.Dropped++
			continue
		}
		p.Rule.ID = nil
		p.Rule.Source = attribution.SourceProposed
		if _, err := ad.NewEngine([]attribution.Rule{p.Rule}); err != nil {
			resp.Dropped++
			continue
		}
		resp.Proposals = append(resp.Proposals, p)
		combined = append(combined, p.Rule)
	}
	eng, err := ad.NewEngine(combined)
	if err != nil {
		return ErrInternal(err)
	}
	resp.CoverageIfApplied = attrCoverage(ac.cost, eng)
	writeJSON(w, http.StatusOK, resp)
	return nil
}
