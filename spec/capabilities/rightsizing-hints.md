# Capability: Rightsizing hints (Phase 3)

## What It Does
Lists deterministic savings hints: VMs with sustained low CPU, unattached persistent disks, and reserved-but-unused static IPs, each with the evidence window and the weekly cost at stake.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| VM CPU % per week | `metrics-block.md` rows (last N weeks) | store | yes |
| Disk / IP inventory | Compute API `disks.aggregatedList`, `addresses.aggregatedList` (read-only, `roles/compute.viewer`) | Compute Engine API | yes |
| Thresholds | `settings`: `rightsizing.cpu_pct` (15), `rightsizing.weeks` (4) | user-editable | yes |
| Cost of the resource | `snapshot_rows` by resource name | store | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Hints | `GET /api/v1/rightsizing` | Rightsizing tab |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| Compute Engine API | list disks (status `READY`, no `users`), list addresses (status `RESERVED`) | source failed + hint (`roles/compute.viewer`, enable `compute.googleapis.com`) |

## Business Rules
- Low-CPU hint when the weekly mean CPU % < threshold for every one of the last N fetched weeks (missing weeks break the streak and are shown).
- Each hint shows the evidence (per-week values), the resource's net cost last week, and a suggested action text (no automatic changes).
- Hints are advisory; dismissing one hides it until evidence changes.

## Success Criteria
- [ ] Unit: 4 weeks at 7 %, 17 %, 6 %, 8 % → no hint (one week above); 4 weeks all < 15 % → hint with 4 evidence points.
- [ ] Real data: the tab lists every VM whose last-4-week means are all < 15 % (expected ≥ 2 in the known environment) with their weekly cost.
