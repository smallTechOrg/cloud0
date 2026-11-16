package httpapi

import "github.com/smallTechOrg/cloud0/internal/attribution"

// AttributionDeps is everything the /attribution, /cost/by-*, /targets and
// /rightsizing handlers need beyond Deps.Store. main wires it once with the
// concrete implementations (attribution.NewEngine, NewSeeder, NewAllocator,
// NewLabelSuggester, NewDetector, NewInventory, and NewOpenRouterProposer or
// attribution.NoopProposer{}). Nil means those endpoints answer 503
// attribution_not_configured.
type AttributionDeps struct {
	NewEngine attribution.EngineFactory
	Seeder    attribution.Seeder
	Allocator attribution.Allocator
	Labels    attribution.LabelSuggester
	Detector  attribution.Detector
	Inventory attribution.InventoryProvider
	Proposer  attribution.Proposer
}
