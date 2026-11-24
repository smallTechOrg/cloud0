# iac-export: map a live GCP project to Terraform

Two standard-library Python scripts that turn whatever is already running in a GCP project into Terraform (or OpenTofu) that `plan`s clean, so the project can be run from a repository from then on. Nothing in them is specific to one organisation.

| Script | Does |
|---|---|
| `iac_export.py` | Reads Cloud Asset Inventory and IAM policies through `gcloud` and writes `import` blocks, one file per resource kind. That covers about 50 kinds: APIs, service accounts and custom roles, Workload Identity, secrets (containers), Cloud Run services/jobs/domain mappings, Artifact Registry, buckets, VPC/firewalls/load balancers, VMs and disks, Cloud SQL instances/databases/users, Scheduler, Pub/Sub, Eventarc, log sinks, alert policies, uptime checks, notification channels, dashboards, org policies. It also writes IAM members (**user permissions as code**) for the project and for Cloud Run services, buckets, secrets and service accounts. |
| `fix_generated.py` | Cleans `plan -generate-config-out` output so it validates and plans with zero diff. See "What the fixer does" below. |

## Use

```sh
mkdir -p infra/gcp/<PROJECT_ID> && cd infra/gcp/<PROJECT_ID>
# versions.tf: google provider + a GCS backend (see the infra repo template or cloud0's infra/terraform)
python3 <cloud0>/tools/iac-export/iac_export.py --project <PROJECT_ID> --out . [--exclude-ids exclude-ids.txt]
tofu init
tofu plan -generate-config-out=generated.tf     # writes resource blocks from the live state
python3 <cloud0>/tools/iac-export/fix_generated.py generated.tf   # exits 2 if credentials remain
tofu plan                                         # iterate until: N to import, 0 to add, 0 to change, 0 to destroy
tofu apply                                        # records state only; changes nothing
```

`--exclude-ids` takes import ids (one per line) that another configuration already manages, such as cloud0's own `infra/terraform`, so nothing is managed twice.

## Skipped on purpose

State churn (revisions, executions, images, data tables, backups, snapshots), secret material (service-account keys, secret versions, API keys), and Google-owned objects (auto-mode default subnets, Cloud Run egress addresses, system log sinks, default service accounts). The summary line lists every skipped kind with a count.

## What the fixer does

- Drops empty values the API echoes back, which conflict with their siblings, while keeping the ones that mean something (Pub/Sub `ttl = ""`, blocks that are empty in the API's own answer such as `replication { auto {} }`).
- Splits ownership with app deploy pipelines: Cloud Run services and jobs ignore `template`, so each app keeps rolling out its own revisions, while Terraform owns the service, ingress, IAM and domain mapping.
- Ignores known provider/API quirks per type, each with the reason written into the `lifecycle` block.
- **Redacts credentials.** Env values that look like keys, tokens, passwords, private keys, URLs with `user:password@` or long base64 blobs are replaced by a placeholder. The run **fails** if anything credential-like is left, so generated secrets are never committed.

## Keep it in code afterwards

Re-run the exporter into a scratch directory now and then. Any import id that isn't in your `_imports.tf` is a resource someone created by hand. `tofu plan` shows hand edits to managed resources.
