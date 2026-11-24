# cloud0 infrastructure (Terraform / OpenTofu)

This directory codifies cloud0's own GCP footprint so it can be reviewed, recreated in another project, and changed through pull requests instead of one-off `gcloud` commands. It works with Terraform ≥ 1.6 or OpenTofu ≥ 1.6. All values come from `terraform.tfvars`, so nothing here is tied to one organisation.

## What it manages

| File | Resources |
|---|---|
| `main.tf` | Required APIs (never disabled on destroy), the Artifact Registry Docker repository |
| `wif.tf` | cloud0's GitHub OIDC provider (keyless deploys, limited to `push` / `workflow_dispatch` on `deploy_refs`) and this repository's *additive* binding on the deployer account |
| `labels.tf` | The least-privilege `cloud0LabelWriter` role, the `cloud0-labels` account and its grant |
| `secrets.tf` | Secret **containers** for the labels key (and optionally the alerts key) and the runtime account's access |
| `imports.tf` | `import` blocks that adopt resources created earlier by `deploy/provision.sh` |

**Deliberately not managed here:**
- The Cloud Run service and migrate job. The deploy workflow ships them per commit, and Terraform would fight it.
- Cloud SQL. The instance may be shared, and a database password must never sit in state.
- Service-account keys and secret *versions*. Add them out of band (`deploy/provision.sh --labels-key …`).
- Shared resources (the Workload Identity pool, the deployer account). They are referenced only. Every binding on them is additive (`*_iam_member`), so other repositories that use them are unaffected.

## Bootstrap (once per project)

```sh
P=<PROJECT_ID>
gcloud storage buckets create gs://$P-tfstate --project $P --location us-central1 \
  --uniform-bucket-level-access --public-access-prevention
gcloud storage buckets update gs://$P-tfstate --versioning

cd infra/terraform
cp terraform.tfvars.example terraform.tfvars   # fill in
tofu init -backend-config="bucket=$P-tfstate"  # or: terraform init …
tofu plan                                      # existing setup: only "to import", 0 to add/change/destroy
tofu apply
```

In a project that already ran `deploy/provision.sh`, the first plan must show only imports. If it shows a change, the code and the live setup disagree: fix the code (or the live resource) before applying. In a fresh project, remove `imports.tf` before the first apply, since there is nothing to import.

Authentication: Application Default Credentials, or for a one-off run `export GOOGLE_OAUTH_ACCESS_TOKEN=$(gcloud auth print-access-token)`.

## Changing infrastructure

Edit the `.tf` files in a pull request. CI checks `fmt` and `validate` (no GCP access). Run `tofu plan` locally and paste the summary in the PR, then `tofu apply` after merge. Labels that cloud0 writes on your *workload* resources (Attribution tab) are separate: copy the Terraform the label preview shows into the repo that manages those resources.
