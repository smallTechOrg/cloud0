# cloud0's own GCP footprint. What is deliberately NOT here:
# - the Cloud Run service and migrate job: deployed per commit by .github/workflows/deploy-cloudrun.yml
#   (image tags change every deploy; Terraform would fight the workflow);
# - Cloud SQL: the instance is shared, and the database user's password must never sit in Terraform state;
# - service-account keys and secret *versions*: secrets are added out of band so they never enter state;
# - shared things (the Workload Identity pool, the deployer account): referenced, and only cloud0's own
#   additive bindings on them are managed, so other repositories that use them are never affected.

data "google_project" "this" {
  project_id = var.project_id
}

locals {
  apis = [
    "run.googleapis.com",
    "artifactregistry.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
    "sqladmin.googleapis.com",
    "secretmanager.googleapis.com",
    "cloudresourcemanager.googleapis.com",
  ]
  runtime_sa_email  = "${var.runtime_service_account}@${var.project_id}.iam.gserviceaccount.com"
  deployer_sa_email = "${var.deployer_service_account}@${var.project_id}.iam.gserviceaccount.com"
}

resource "google_project_service" "apis" {
  for_each = toset(local.apis)

  service            = each.value
  disable_on_destroy = false # never switch an API off because this config changed
}

resource "google_artifact_registry_repository" "images" {
  location      = var.region
  repository_id = var.artifact_repository
  format        = "DOCKER"
  description   = "cloud0 images"

  depends_on = [google_project_service.apis]
}
