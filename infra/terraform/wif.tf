# GitHub Actions → GCP without keys. The pool may be shared with other repositories: it is not managed here.

resource "google_iam_workload_identity_pool_provider" "github" {
  workload_identity_pool_id          = var.wif_pool_id
  workload_identity_pool_provider_id = var.wif_provider_id
  display_name                       = "cloud0 GitHub"

  attribute_mapping = {
    "google.subject"       = "assertion.sub"
    "attribute.repository" = "assertion.repository"
    "attribute.ref"        = "assertion.ref"
  }

  # Only push / workflow_dispatch runs of this repository, on the deploy refs, may authenticate.
  attribute_condition = "assertion.repository=='${var.github_repo}' && (assertion.event_name=='push' || assertion.event_name=='workflow_dispatch') && assertion.ref in [${join(", ", [for r in var.deploy_refs : "'${r}'"])}]"

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

# Additive: lets this repository's workflows impersonate the deployer. Other repositories' bindings on the same
# (shared) account are untouched.
resource "google_service_account_iam_member" "deployer_wif" {
  service_account_id = "projects/${var.project_id}/serviceAccounts/${local.deployer_sa_email}"
  role               = "roles/iam.workloadIdentityUser"
  member             = "principalSet://iam.googleapis.com/projects/${data.google_project.this.number}/locations/global/workloadIdentityPools/${var.wif_pool_id}/attribute.repository/${var.github_repo}"
}
