package billingusage

import (
	"strings"

	"github.com/smallTechOrg/cloud0/internal/gcpsource"
)

// Match is how a Rule's Pattern is compared with sku.description.
type Match string

const (
	// MatchExact requires sku.description == Pattern.
	MatchExact Match = "exact"
	// MatchPrefix requires sku.description to start with Pattern.
	MatchPrefix Match = "prefix"
	// MatchContains requires sku.description to contain Pattern.
	MatchContains Match = "contains"
)

// Rule maps billing SKUs (service.description + a sku.description pattern)
// to one Metrics-block cell. Usage is summed from
// usage.amount_in_pricing_units, whose pricing unit must equal PricingUnit.
type Rule struct {
	Group       string   // gcpsource.Group*
	Metric      string   // gcpsource.M*
	Unit        string   // cell unit shown in the UI
	Services    []string // accepted service.description values
	Match       Match
	Pattern     string
	PricingUnit string // expected usage.pricing_unit; other units are skipped
	Why         string // why these SKUs reproduce the sheet's column
}

// Rules is the SKU → cell classification, derived from the real SKU
// descriptions in the detailed billing export (inspected for ISO 2026-W42).
//
// Units: the sheet labels these columns "GB" but its numbers are the billing
// export's pricing units (GiB) — e.g. Cloud Run intercontinental 0.86 is
// exactly 0.861 GiB — so the cells carry the GiB pricing-unit amount with the
// sheet's "GB" label.
var Rules = []Rule{
	{
		Group: gcpsource.GroupNetworking, Metric: gcpsource.MLBHours, Unit: "hours",
		Services: []string{"Networking", "Compute Engine"}, Match: MatchContains,
		Pattern: "Forwarding Rule", PricingUnit: "hour",
		Why: "forwarding-rule hours as billed: the 'Forwarding Rule Minimum' SKU bills the first 5 rules as one unit per hour " +
			"(168 = 1 minimum charge x 168 h); 'Additional' rules add their own hours",
	},
	{
		Group: gcpsource.GroupNetworking, Metric: gcpsource.MOutboundGB, Unit: "GB",
		Services: []string{"Networking"}, Match: MatchContains,
		Pattern: "Load Balancer Outbound Data Processing", PricingUnit: "gibibyte",
		Why: "load-balancer outbound data processed (the sheet's Networking Outbound column)",
	},
	{
		Group: gcpsource.GroupNetworking, Metric: gcpsource.MInboundGB, Unit: "GB",
		Services: []string{"Networking"}, Match: MatchContains,
		Pattern: "Load Balancer Inbound Data Processing", PricingUnit: "gibibyte",
		Why: "load-balancer inbound data processed (the sheet's Networking Inbound column)",
	},
	{
		Group: gcpsource.GroupCloudRun, Metric: gcpsource.MEgressInterGB, Unit: "GB",
		Services: []string{"Cloud Run"}, Match: MatchPrefix,
		Pattern: "Cloud Run Network Internet Data Transfer Out Intercontinental", PricingUnit: "gibibyte",
		Why: "Cloud Run internet egress across continents (all destination variants); inter-region (Google-internal) transfer is excluded",
	},
	{
		Group: gcpsource.GroupGCS, Metric: gcpsource.MDownloadAPACGB, Unit: "GB",
		Services: []string{"Cloud Storage"}, Match: MatchExact,
		Pattern: "Download APAC", PricingUnit: "gibibyte",
		Why: "Cloud Storage internet download billed to APAC destinations",
	},
}

// Matches reports whether a billing line with this service and SKU
// description belongs to the rule.
func (r Rule) Matches(service, sku string) bool {
	okSvc := false
	for _, s := range r.Services {
		if s == service {
			okSvc = true
			break
		}
	}
	if !okSvc {
		return false
	}
	switch r.Match {
	case MatchExact:
		return sku == r.Pattern
	case MatchPrefix:
		return strings.HasPrefix(sku, r.Pattern)
	case MatchContains:
		return strings.Contains(sku, r.Pattern)
	}
	return false
}

// Classify returns the first rule matching a billing line, or false.
func Classify(service, sku string) (Rule, bool) {
	for _, r := range Rules {
		if r.Matches(service, sku) {
			return r, true
		}
	}
	return Rule{}, false
}

// sqlPredicate renders the rule as a BigQuery boolean expression. Patterns
// and services are package constants (validated by tests to contain no
// quote or backslash), so they are inlined as string literals.
func (r Rule) sqlPredicate() string {
	svcs := make([]string, len(r.Services))
	for i, s := range r.Services {
		svcs[i] = "'" + s + "'"
	}
	var sku string
	switch r.Match {
	case MatchExact:
		sku = "sku.description = '" + r.Pattern + "'"
	case MatchPrefix:
		sku = "STARTS_WITH(sku.description, '" + r.Pattern + "')"
	default:
		sku = "STRPOS(sku.description, '" + r.Pattern + "') > 0"
	}
	return "(service.description IN (" + strings.Join(svcs, ", ") + ") AND " + sku + ")"
}
