# cloud0 — AI-native design

## Conclusion: no AI capability needed

cloud0 replaces a hand-maintained spreadsheet with deterministic queries over billing, metrics and error data. Every number it shows must be reproducible, auditable (exact SQL / Monitoring filter on click) and identical on every refresh of a final week. An LLM anywhere on the data path would add cost, latency and nondeterminism with no product upside, and the user explicitly ruled out NL Q&A, chat, digests and AI in the product. The product is conventional end to end; the build process is agentic (parallel slices, independent gates) as always.

## Opportunities considered and rejected (one line each)

| Opportunity | Catalogue pattern it would use | Decision and rationale |
|---|---|---|
| Natural-language questions over cost data ("why did Cloud Run jump?") | #22 LLM-generated code execution over the snapshot tables, wrapped in a ReAct loop (#17 + #5) | **Rejected** — explicitly out of scope at intake; the fixed Cost/Metrics tables plus drill-down answer the team's weekly questions deterministically. If ever revisited, #22 (generated SQL over SQLite) is the right shape, never an op-list. |
| Explaining anomaly flags in prose | #1 Prompt chaining over the flagged row + drill-down | **Rejected** — the flag already carries the exact Δ and Δ%, and one click shows the SKU/resource that moved; prose would restate deterministic facts. |
| Classifying BigQuery/Monitoring errors into fix hints | #2 Routing | **Rejected** — the error space is small and known (permission denied, API disabled, table not found, quota, credentials); a lookup table in `internal/billing/hints.go` is exact and free. |
| Inferring product/environment attribution rules from resource names and labels | #5 Tool use (one call) + #18 Guardrails (schema-validated JSON) + #13 Human-in-the-loop (user accepts/edits proposals) | **Deferred, optional, off by default (Phase 3)** — the deterministic seeder (GCP labels > project > name tokens) covers today's naming; an LLM pass can only *propose* rules for the user to accept in the rules editor. See "Phase 3 optional rule proposer" below. |
| Rightsizing recommendations | #6 Planning / #17 Reasoning | **Rejected** — "CPU < 15% for N weeks", "disk unattached", "static IP unused" are threshold rules over Monitoring data; deterministic and explainable. |
| Weekly summary / digest text | #1 Prompt chaining | **Rejected** — digests (Slack/email) are explicitly out of scope; the Summary surface is a table of numbers vs targets. |
| Label suggestions for unlabelled resources | #5 Tool use | **Rejected** — suggestions are derived from the matched attribution rule (product/env tokens) and rendered as the exact `gcloud` label command; no generation needed. |
| Conversational memory | #8 Memory | **Not applicable** — there is no conversation surface. |

## Phase 3 optional rule proposer (the only LLM touchpoint, pluggable)

- Interface in Go: `attribution.RuleProposer` with `Propose(ctx, unattributed []ResourceRef) ([]ProposedRule, error)`. Default implementation `NoopProposer` returns an empty list and the UI shows "LLM proposals disabled (set CLOUD0_OPENROUTER_API_KEY to enable)".
- `OpenRouterProposer` is selected only when `CLOUD0_OPENROUTER_API_KEY` is set **and** the user clicks "Propose rules with LLM" — never automatically, never on a data path. Model from `CLOUD0_LLM_MODEL` (default `anthropic/claude-sonnet-4-6`, verified with one minimal real call when the phase is built, per `tech-stack.md`).
- Composition: single tool-less call (pattern #1 degenerate to one step) → JSON array of `{match_kind, pattern, product, environment, confidence, rationale}` validated against a schema (#18) → shown as *proposals* the user accepts/edits/rejects (#13). Accepted proposals become ordinary rules indistinguishable from hand-written ones; the proposer never writes rules itself.
- Prompt is a file (`internal/attribution/prompts/propose_rules.md`), inputs are resource names/labels/project IDs only (no costs), the call is logged with latency and token counts, and the whole package is absent from the binary's hot path when disabled.
- No key, no model slug, no OpenRouter dependency is required until Phase 3 is built; Phases 1–2 ship with `NoopProposer` only (the interface is introduced in Phase 3, not before — no premature abstraction).
