package httpapi

// registerAttributionRoutes wires the Phase 3 endpoints of spec/api.md
// ('Attribution additions'), one file per owning worker:
//
//	attribution_rules.go        rules, seed, unattributed, explain, label suggestions, propose
//	attribution_breakdown.go    cost/by-product, cost/by-environment (+ breakdown filters)
//	attribution_targets.go      targets
//	attribution_rightsizing.go  rightsizing
func (d Deps) registerAttributionRoutes(r *router) {
	d.registerAttributionRules(r)
	d.registerAttributionBreakdown(r)
	d.registerAttributionTargets(r)
	d.registerAttributionRightsizing(r)
}
