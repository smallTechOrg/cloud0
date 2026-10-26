# Capability: Attribution rules engine (Phase 3)

## What It Does
Assigns every snapshot row to a product and an environment using an ordered, deterministic rule set (GCP labels first, then project, then resource-name tokens), auto-seeded from the resources actually seen, editable in a rules UI, with an unattributed list and an optional, off-by-default LLM proposer.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Snapshot rows | `snapshot_rows` (+ labels captured from the billing export: migration 0003 adds `labels_json`, `system_labels_json` to `snapshot_rows`, and the query selects `labels`, `system_labels`) | store | yes |
| Rules | `attribution_rules` (migration 0003): id, priority, match_kind (`label`, `project`, `name_token`, `global_name_regex`), pattern, product, environment, source (`seed`, `user`, `llm_accepted`), enabled | store | yes |
| Proposals | `ProposedRule[]` | `RuleProposer` (`spec/agent.md`) | optional |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Per-row attribution (product, environment, matched rule id) | computed at read time, cached per snapshot in `attribution_results` | `product-env-breakdown.md`, provenance |
| Unattributed list with reasons | `GET /api/v1/attribution/unattributed?snapshot_id=` | Attribution tab |
| Rules CRUD | `GET/PUT /api/v1/attribution/rules` | rules editor |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| OpenRouter (only when `CLOUD0_OPENROUTER_API_KEY` set and the user clicks "Propose rules with LLM") | one chat completion returning JSON proposals | error shown; no rules change |

## Business Rules
- Precedence: label `product`/`app` and `env`/`environment` on the resource → project-level default (configurable per project) → name tokens (first matching token rule by priority) → `(unattributed)`.
- Seeder: derives candidate product tokens from resource names (split on `-`, `_`, `/`, digits stripped), environment tokens `prod|production|staging|stage|dev|beta|test`, and label values seen; proposes rules the user reviews once; seeded rules carry `source: seed`.
- Rows with no resource name are attributed by SKU-level rules (e.g. a forwarding-rule SKU → the product owning the LB) or stay unattributed with reason "no resource name".
- Attribution is recomputed when rules change; snapshots are never modified. Provenance of a product cell shows the matched rule.
- The LLM proposer only proposes; acceptance writes a normal rule with `source: llm_accepted`. Disabled (`NoopProposer`) unless the key is present; the UI states this.

## Success Criteria
- [ ] Unit: a label `env=prod` beats a `staging` token in the name; project default applies when no label and no token; rule priority ordering is stable.
- [ ] Real data: the seeded rule set attributes ≥ 90 % of the smoke week's net; Σ product nets + unattributed = Total ± 0.005.
- [ ] `POST /attribution/propose` with the key unset → 200 `{enabled:false, proposals:[]}` and no outbound call (unit test with a fake HTTP transport asserting zero requests).
- [ ] e2e: edit a rule, Save → breakdown updates; unattributed list shows reasons.
