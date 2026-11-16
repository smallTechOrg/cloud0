package attribution

import (
	"sort"
	"strings"
)

// Resource inventory for the "Assign products" UI: every billed resource of a
// snapshot, grouped so that one decision covers everything that belongs
// together (a VM and its boot disk share a name), with the current attribution
// verdict, the GCP labels actually present, and — when cloud0 can write them —
// the exact GCP label targets.

// Attribution sources reported per group.
const (
	SourceLabel  = "label"  // decided by a product label on the resource
	SourceRule   = "rule"   // decided by a stored rule
	SourceNone   = "none"   // unattributed
	SourceShared = "shared" // marked shared
	resKindName  = "resource"
	resKindSKU   = "sku"
	maxLabelKeys = 12
	noNamePrefix = "(no resource name) "
	groupKeySep  = "\x1f"
)

// LabelTarget identifies one GCP resource whose labels cloud0 can read/write.
type LabelTarget struct {
	Project  string `json:"project"`
	Type     string `json:"type"` // Type* constant
	Name     string `json:"name"`
	Location string `json:"location,omitempty"` // zone or region; empty for global resources
	// Resource is the billing resource name this target was derived from.
	Resource string `json:"resource"`
	// Writable is false for resource types cloud0 cannot label.
	Writable bool `json:"writable"`
	// Note explains why a target is not writable.
	Note string `json:"note,omitempty"`
}

// ResourceMember is one billing resource (or resource-less SKU) of a group.
type ResourceMember struct {
	Key          string  `json:"key"`
	Kind         string  `json:"kind"` // resource | sku
	ResourceName string  `json:"resource_name"`
	Service      string  `json:"service"`
	SKU          string  `json:"sku,omitempty"`
	Net          float64 `json:"net"`
}

// ResourceGroup is one row of the Assign UI.
type ResourceGroup struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"` // resource | sku
	ProjectID string            `json:"project_id"`
	Services  []string          `json:"services"`
	Net       float64           `json:"net"`
	SharePct  float64           `json:"share_pct"`
	Labels    map[string]string `json:"labels"`
	Product   string            `json:"product"`
	Env       string            `json:"environment"`
	Shared    bool              `json:"shared"`
	Source    string            `json:"source"`
	RuleID    *int64            `json:"rule_id"`
	// Match is the rule match that covers every member of the group.
	Match   Match            `json:"match"`
	Members []ResourceMember `json:"members"`
	Targets []LabelTarget    `json:"targets"`
	// Suggested is the engine's low-effort guess for unattributed groups.
	Suggested *Suggested `json:"suggested"`
}

// ResourceProduct is one product of the catalog the UI offers in its dropdown.
type ResourceProduct struct {
	Product   string  `json:"product"`
	Net       float64 `json:"net"`
	Resources int     `json:"resources"`
}

// ResourcesResponse is the body of GET /api/v1/attribution/resources.
type ResourcesResponse struct {
	Week           string            `json:"week"`
	Scope          string            `json:"scope"`
	Snapshot       *SnapshotRef      `json:"snapshot"`
	LabelsCaptured bool              `json:"labels_captured"`
	TotalNet       float64           `json:"total_net"`
	Count          int               `json:"count"`
	Truncated      bool              `json:"truncated"`
	Groups         []ResourceGroup   `json:"groups"`
	Products       []ResourceProduct `json:"products"`
	Environments   []string          `json:"environments"`
	// LabelWriter reports whether GCP label writes are configured.
	LabelWriter LabelWriterInfo `json:"label_writer"`
}

// LabelWriterInfo says whether cloud0 can write GCP labels.
type LabelWriterInfo struct {
	Enabled bool   `json:"enabled"`
	Hint    string `json:"hint,omitempty"`
}

type resMember struct {
	key        string
	kind       string
	project    string
	name       string
	global     string
	service    string
	sku        string
	net        float64
	bestNet    float64
	bestSvc    string
	bestSKU    string
	haveBest   bool
	labels     map[string]string
	prod, env  string
	prodRule   *int64
	envRule    *int64
	prodNet    float64
	envNet     float64
	shared     bool
	sharedNet  float64
	sharedRule *int64
	first      Row
}

// BuildResources groups the snapshot rows and evaluates each group. The
// suggest callback (may be nil) supplies the unattributed hint.
func BuildResources(rows []CostRow, eng Engine, suggest func(Row) *Suggested) (groups []ResourceGroup, products []ResourceProduct, envs []string, total float64) {
	labelRules := map[int64]bool{}
	if eng != nil {
		for _, r := range eng.Rules() {
			if r.ID != nil && r.Match.Field == FieldLabel {
				labelRules[*r.ID] = true
			}
		}
	}

	members := map[string]*resMember{}
	var order []string
	for _, cr := range rows {
		total += cr.Net
		name := strings.TrimSpace(cr.ResourceName)
		var key, kind string
		if name != "" {
			kind, key = resKindName, resKindName+groupKeySep+cr.ProjectID+groupKeySep+name
		} else {
			kind, key = resKindSKU, resKindSKU+groupKeySep+cr.ProjectID+groupKeySep+cr.Service+groupKeySep+cr.SKU
		}
		m := members[key]
		if m == nil {
			m = &resMember{key: key, kind: kind, project: cr.ProjectID, name: name, service: cr.Service, sku: cr.SKU,
				labels: map[string]string{}, first: cr.Row}
			members[key] = m
			order = append(order, key)
		}
		m.net += cr.Net
		if !m.haveBest || cr.Net > m.bestNet {
			m.bestNet, m.bestSvc, m.bestSKU, m.haveBest = cr.Net, cr.Service, cr.SKU, true
		}
		if m.global == "" {
			m.global = cr.ResourceGlobalName
		}
		for k, v := range cr.Labels {
			if len(m.labels) < maxLabelKeys {
				if _, ok := m.labels[k]; !ok {
					m.labels[k] = v
				}
			}
		}
		if eng == nil {
			continue
		}
		ev := eng.Evaluate(cr.Row)
		if ev.Shared && (m.sharedNet == 0 || cr.Net > m.sharedNet) {
			m.shared, m.sharedNet, m.sharedRule = true, cr.Net, copyID(ev.RuleID)
		}
		if ev.Product != "" && !ev.Shared && (m.prod == "" || cr.Net > m.prodNet) {
			m.prod, m.prodNet, m.prodRule = ev.Product, cr.Net, copyID(ev.RuleID)
		}
		if ev.Environment != "" && (m.env == "" || cr.Net > m.envNet) {
			m.env, m.envNet, m.envRule = ev.Environment, cr.Net, copyID(ev.EnvironmentRuleID)
		}
	}

	// Group by project + last path segment of the name, so a VM
	// ("projects/N/instances/x") and its disk ("x") are one decision.
	byGroup := map[string]*ResourceGroup{}
	var gorder []string
	for _, k := range order {
		m := members[k]
		var gid, display string
		var match Match
		if m.kind == resKindName {
			seg := strings.ToLower(lblLastSegment(m.name))
			if seg == "" {
				seg = strings.ToLower(m.name)
			}
			gid = resKindName + groupKeySep + m.project + groupKeySep + seg
			display = lblLastSegment(m.name)
			if display == "" {
				display = m.name
			}
			match = Match{Field: FieldResourceName, Op: OpEquals, Value: lblLastSegment(m.name)}
		} else {
			gid = m.key
			display = noNamePrefix + m.service + " — " + m.sku
			match = Match{Field: FieldSKU, Op: OpEquals, Value: m.sku}
		}
		g := byGroup[gid]
		if g == nil {
			g = &ResourceGroup{ID: gid, Name: display, Kind: m.kind, ProjectID: m.project, Labels: map[string]string{},
				Match: match, Members: []ResourceMember{}, Targets: []LabelTarget{}, Services: []string{}, Source: SourceNone}
			byGroup[gid] = g
			gorder = append(gorder, gid)
		}
		g.Net += m.net
		g.Members = append(g.Members, ResourceMember{Key: m.key, Kind: m.kind, ResourceName: m.name, Service: m.service, SKU: m.sku, Net: round2(m.net)})
		if !containsStr(g.Services, m.service) {
			g.Services = append(g.Services, m.service)
		}
		for k, v := range m.labels {
			if _, ok := g.Labels[k]; !ok {
				g.Labels[k] = v
			}
		}
		if m.kind == resKindName {
			if t, ok := resolveTarget(m); ok {
				g.Targets = append(g.Targets, t)
			}
		}
	}

	prodNet := map[string]*ResourceProduct{}
	envSet := map[string]bool{}
	for _, gid := range gorder {
		g := byGroup[gid]
		// Second pass: pick the verdict from the biggest deciding member.
		var bestProd, bestShared *resMember
		var bestEnv *resMember
		for _, mm := range g.Members {
			m := members[mm.Key]
			if m.prod != "" && (bestProd == nil || m.net > bestProd.net) {
				bestProd = m
			}
			if m.shared && (bestShared == nil || m.net > bestShared.net) {
				bestShared = m
			}
			if m.env != "" && (bestEnv == nil || m.net > bestEnv.net) {
				bestEnv = m
			}
		}
		switch {
		case bestProd != nil:
			g.Product, g.RuleID = bestProd.prod, bestProd.prodRule
			g.Source = SourceRule
			if g.RuleID != nil && *g.RuleID == BuiltinRuleID {
				g.Source = SourceLabel
			}
		case bestShared != nil:
			g.Shared, g.RuleID, g.Source = true, bestShared.sharedRule, SourceShared
		}
		if bestEnv != nil {
			g.Env = bestEnv.env
		}
		if g.Source == SourceNone && suggest != nil {
			g.Suggested = suggest(members[g.Members[0].Key].first)
		}
		g.Net = round2(g.Net)
		sort.Strings(g.Services)
		sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].Net > g.Members[j].Net })
		sort.Slice(g.Targets, func(i, j int) bool { return g.Targets[i].Name+g.Targets[i].Type < g.Targets[j].Name+g.Targets[j].Type })
		g.Targets = dedupTargets(g.Targets)
		if g.Product != "" {
			ps := prodNet[g.Product]
			if ps == nil {
				ps = &ResourceProduct{Product: g.Product}
				prodNet[g.Product] = ps
			}
			ps.Net += g.Net
			ps.Resources++
		}
		if g.Env != "" {
			envSet[g.Env] = true
		}
	}

	groups = make([]ResourceGroup, 0, len(gorder))
	for _, gid := range gorder {
		g := byGroup[gid]
		if total > 0 {
			g.SharePct = round2(100 * g.Net / total)
		}
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Net != groups[j].Net {
			return groups[i].Net > groups[j].Net
		}
		return groups[i].Name < groups[j].Name
	})
	for _, ps := range prodNet {
		ps.Net = round2(ps.Net)
		products = append(products, *ps)
	}
	sort.Slice(products, func(i, j int) bool {
		if products[i].Net != products[j].Net {
			return products[i].Net > products[j].Net
		}
		return products[i].Product < products[j].Product
	})
	for e := range envSet {
		envs = append(envs, e)
	}
	sort.Strings(envs)
	if products == nil {
		products = []ResourceProduct{}
	}
	if envs == nil {
		envs = []string{}
	}
	return groups, products, envs, round2(total)
}

func dedupTargets(in []LabelTarget) []LabelTarget {
	out := in[:0]
	seen := map[string]bool{}
	for _, t := range in {
		k := t.Project + "|" + t.Type + "|" + t.Location + "|" + t.Name
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func round2(v float64) float64 {
	if v < 0 {
		return -float64(int64(-v*100+0.5)) / 100
	}
	return float64(int64(v*100+0.5)) / 100
}

// writableTypes are the resource types cloud0's label writer supports.
func writableType(t string) bool {
	switch t {
	case TypeComputeInstance, TypeComputeDisk, TypeCloudRunService, TypeCloudRunJob, TypeGCSBucket, TypeCloudSQLInstance:
		return true
	}
	return false
}

// resolveTarget derives the GCP label target of a named billing resource.
func resolveTarget(m *resMember) (LabelTarget, bool) {
	pg := parseGlobalName(m.global)
	rtype := pg.Resource
	if rtype == TypeUnknown {
		rtype = typeFromService(m.bestSvc, m.bestSKU)
	}
	if rtype == TypeUnknown {
		return LabelTarget{}, false
	}
	name := pg.Name
	if name == "" || lblAllDigits(name) {
		if seg := lblLastSegment(m.name); seg != "" && !lblAllDigits(seg) {
			name = seg
		}
	}
	project := m.project
	if project == "" {
		project = pg.Project
	}
	loc := pg.Zone
	if loc == "" {
		loc = pg.Region
	}
	t := LabelTarget{Project: project, Type: rtype, Name: name, Location: loc, Resource: m.name, Writable: true}
	switch {
	case !writableType(rtype):
		t.Writable, t.Note = false, "cloud0 cannot write labels on this resource type; use the gcloud command or the console."
	case name == "" || project == "":
		t.Writable, t.Note = false, "could not determine the resource name or project from the billing row."
	case (rtype == TypeComputeInstance || rtype == TypeComputeDisk) && !strings.Contains(m.global, "/zones/"):
		t.Writable, t.Note = false, "no zone in the billing row, so the Compute resource cannot be located."
	case (rtype == TypeCloudRunService || rtype == TypeCloudRunJob) && loc == "":
		t.Writable, t.Note = false, "no region in the billing row, so the Cloud Run resource cannot be located."
	}
	return t, true
}

// LabelAdds returns the labels that need writing for a target so that it
// carries product (and env when non-empty); existing values are not skipped —
// the writer reports the diff.
func LabelAdds(product, env string) map[string]string {
	out := map[string]string{}
	if v := sanitizeLabelValue(product); v != "" {
		out[ProductLabelKey] = v
	}
	if v := sanitizeLabelValue(env); v != "" {
		out[suggestEnvKey] = v
	}
	return out
}

// LabelCommand renders the gcloud equivalent of writing labels on a target and
// the risk note for its type. cmd is nil when the type has no command template.
func LabelCommand(rtype, project, name, location string, labels map[string]string) (cmd *string, risk string) {
	zone, region := "", ""
	if rtype == TypeComputeInstance || rtype == TypeComputeDisk {
		zone = location
	} else {
		region = location
	}
	return buildCommand(rtype, project, name, zone, region, labels), riskText(rtype)
}
