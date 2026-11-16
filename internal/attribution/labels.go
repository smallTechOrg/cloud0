package attribution

import (
	"sort"
	"strings"
)

type labelSuggester struct{}

// NewLabelSuggester returns the pure LabelSuggester (spec/api.md ->
// label-suggestions). cloud0 never executes the commands it builds.
func NewLabelSuggester() LabelSuggester { return labelSuggester{} }

type resKey struct{ project, name string }

// resAgg accumulates one (project, resource_name) across SKUs/rows.
type resAgg struct {
	key         resKey
	globalName  string
	service     string
	sku         string
	net         float64
	bestNet     float64 // net of the row that supplied global/service/sku
	haveRow     bool
	shared      bool
	sharedBuilt bool
	builtin     bool
	product     string
	productRule *int64
	productNet  float64
	env         string
	envRule     *int64
	envNet      float64
}

func isNonLabelRule(id *int64, labelRules map[int64]bool) bool {
	return id != nil && *id != BuiltinRuleID && !labelRules[*id]
}

func copyID(id *int64) *int64 {
	if id == nil {
		return nil
	}
	v := *id
	return &v
}

// Suggest implements LabelSuggester.
func (labelSuggester) Suggest(rows []CostRow, eng Engine) ([]LabelSuggestion, int) {
	items := []LabelSuggestion{}
	if eng == nil {
		return items, 0
	}
	labelRules := map[int64]bool{}
	for _, r := range eng.Rules() {
		if r.ID != nil && r.Match.Field == FieldLabel {
			labelRules[*r.ID] = true
		}
	}

	aggs := map[resKey]*resAgg{}
	var order []resKey
	for _, cr := range rows {
		name := strings.TrimSpace(cr.ResourceName)
		if name == "" {
			continue
		}
		k := resKey{cr.ProjectID, name}
		a := aggs[k]
		if a == nil {
			a = &resAgg{key: k}
			aggs[k] = a
			order = append(order, k)
		}
		a.net += cr.Net
		if !a.haveRow || cr.Net > a.bestNet {
			a.bestNet, a.service, a.sku, a.haveRow = cr.Net, cr.Service, cr.SKU, true
		}
		if a.globalName == "" {
			a.globalName = cr.ResourceGlobalName
		}
		ev := eng.Evaluate(cr.Row)
		if ev.RuleID != nil && *ev.RuleID == BuiltinRuleID || ev.EnvironmentRuleID != nil && *ev.EnvironmentRuleID == BuiltinRuleID {
			a.builtin = true
		}
		if ev.Shared {
			a.shared = true
			if ev.RuleID != nil && *ev.RuleID == BuiltinRuleID {
				a.sharedBuilt = true
			}
		}
		if ev.Product != "" && !ev.Shared && isNonLabelRule(ev.RuleID, labelRules) && (a.product == "" || cr.Net > a.productNet) {
			a.product, a.productRule, a.productNet = ev.Product, copyID(ev.RuleID), cr.Net
		}
		if ev.Environment != "" && isNonLabelRule(ev.EnvironmentRuleID, labelRules) && (a.env == "" || cr.Net > a.envNet) {
			a.env, a.envRule, a.envNet = ev.Environment, copyID(ev.EnvironmentRuleID), cr.Net
		}
	}

	done := 0
	for _, k := range order {
		a := aggs[k]
		if a.shared {
			if a.sharedBuilt {
				done++
			}
			continue
		}
		labels := map[string]string{}
		missing := []string{}
		var prodRule, envRule *int64
		if v := sanitizeLabelValue(a.product); v != "" {
			labels[suggestProductKey] = v
			missing = append(missing, suggestProductKey)
			prodRule = a.productRule
		}
		if v := sanitizeLabelValue(a.env); v != "" {
			labels[suggestEnvKey] = v
			missing = append(missing, suggestEnvKey)
			envRule = a.envRule
		}
		if len(missing) == 0 {
			if a.builtin {
				done++
			}
			continue
		}

		pg := parseGlobalName(a.globalName)
		rtype := pg.Resource
		if rtype == TypeUnknown {
			rtype = typeFromService(a.service, a.sku)
		}
		cmdName := pg.Name
		if cmdName == "" || pg.Resource == TypeUnknown {
			cmdName = k.name
		}
		// Compute global names end in the numeric id (…/instances/4646…), but
		// gcloud wants the NAME: prefer the billing resource name's last
		// segment when the global name's last segment is purely numeric.
		if lblAllDigits(cmdName) {
			if seg := lblLastSegment(k.name); seg != "" && !lblAllDigits(seg) {
				cmdName = seg
			}
		}
		project := k.project
		if project == "" {
			project = pg.Project
		}
		var loc *string
		switch {
		case pg.Zone != "":
			z := pg.Zone
			loc = &z
		case pg.Region != "":
			r := pg.Region
			loc = &r
		}
		cmd := buildCommand(rtype, project, cmdName, pg.Zone, pg.Region, labels)
		risk := riskText(rtype)
		if cmd == nil {
			risk += locationNote(rtype)
		}
		var gn *string
		if a.globalName != "" {
			g := a.globalName
			gn = &g
		}
		items = append(items, LabelSuggestion{
			ProjectID: k.project, ResourceName: k.name, ResourceGlobalName: gn,
			ResourceType: rtype, RegionOrZone: loc, Net: a.net,
			Labels: labels, Missing: missing,
			Basis:   LabelBasis{ProductRuleID: prodRule, EnvironmentRuleID: envRule},
			Command: cmd, Risk: risk,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Net != items[j].Net {
			return items[i].Net > items[j].Net
		}
		if items[i].ResourceName != items[j].ResourceName {
			return items[i].ResourceName < items[j].ResourceName
		}
		return items[i].ProjectID < items[j].ProjectID
	})
	return items, done
}

func lblAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func lblLastSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}
