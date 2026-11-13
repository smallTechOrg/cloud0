# Capability: Label suggestions (Phase 3)

## What It Does
For every resource attributed by a name token or project default (i.e. not by a label), suggests the exact labels to add in GCP so attribution becomes authoritative, with copy-pasteable `gcloud` commands.

## Inputs
| Input | Type | Source | Required |
|---|---|---|---|
| Attribution results with `matched_kind` | store | `attribution-rules.md` | yes |
| Resource type | derived from `resource.global_name` (instances, services, buckets, SQL instances) | store | yes |

## Outputs
| Output | Type | Destination |
|---|---|---|
| Suggestions list | `GET /api/v1/attribution/suggestions?snapshot_id=` | Attribution tab → "Suggested labels" |

## External Calls
| System | Operation | On Failure |
|---|---|---|
| none (read-only; cloud0 never writes labels) | | |

## Business Rules
- Suggest `product=<product>` and `env=<environment>` for resources whose attribution came from tokens/project.
- Command templates per resource type (Compute instance, Cloud Run service/job, GCS bucket, Cloud SQL instance); unknown types show the label pair only.
- Marked done automatically once a later snapshot shows the label present.

## Success Criteria
- [ ] Unit: a VM attributed by token yields a `gcloud compute instances add-labels … --labels=product=…,env=…` command with the correct zone parsed from `global_name`.
- [ ] Real data: suggestions exist for at least one resource in the smoke week; none for label-attributed resources.
