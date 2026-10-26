# Capability: Vendor usage — Groq, Claude (Phase 4)

## What It Does
Fills the Cost table's two vendor columns and the "Groq errors" metrics row with weekly usage from Groq's API and from a local Claude usage CLI runner, stored per week like any other snapshot.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Groq usage | requests, tokens, cost, errors per model per week | Groq API (read-only key `CLOUD0_GROQ_API_KEY`) | when configured |
| Claude usage | tokens/cost per week | a CLI runner command configured by `CLOUD0_CLAUDE_USAGE_CMD` whose stdout is JSON | when configured |
| Week | `WeekID` | Refresh | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| `vendor_usage` rows (migration 0004) | store | Cost table columns `AI Cost (Groq)`, `claude (usage)` (no longer stubs); Vendors tab; Limits tab Groq rows; Metrics "Groq errors" row |
| `run_sources` rows `vendor_groq`, `vendor_claude` | store | run log |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Groq API | usage endpoint for the week window | source failed + hint ("set CLOUD0_GROQ_API_KEY" / "key lacks usage scope"); columns show "—" with the reason |
| Local CLI | exec with timeout 60 s, parse JSON | source failed + hint; never blocks billing refresh |

## Business Rules
- Vendors are read-only integrations; keys live in `.env` only.
- Column semantics: `AI Cost (Groq)` = USD; `claude (usage)` = the unit the runner reports (documented in the column tooltip).
- When unconfigured, the columns stay labelled stubs with the hint "set CLOUD0_GROQ_API_KEY" rather than showing 0.

## Success Criteria
- [ ] With `CLOUD0_GROQ_API_KEY` set: the smoke week has a numeric Groq cost and error count; the run log shows the source.
- [ ] Without the key: refresh still `ok` for billing; vendor source recorded as `skipped` with hint; the columns remain stubs.
- [ ] e2e: Vendors tab shows per-model rows; the Cost table's vendor columns show numbers when configured.
