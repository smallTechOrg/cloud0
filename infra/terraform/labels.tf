# Labels writer: lets the Attribution tab write product/env labels (README → Assign products and labels).
# Least privilege: read + set labels only. No Cloud Run permissions: updating a Cloud Run service needs
# iam.serviceaccounts.actAs, a power to run code as its runtime account, which cloud0 deliberately does not hold.

resource "google_project_iam_custom_role" "label_writer" {
  role_id     = "cloud0LabelWriter"
  title       = "cloud0 label writer"
  description = "Read and set labels on resources for cloud0 attribution. No create/delete/deploy."
  stage       = "GA"
  permissions = [
    "cloudsql.instances.get",
    "cloudsql.instances.update",
    "compute.disks.get",
    "compute.disks.setLabels",
    "compute.instances.get",
    "compute.instances.setLabels",
    "compute.zoneOperations.get",
    "storage.buckets.get",
    "storage.buckets.update",
  ]
}

resource "google_service_account" "labels" {
  account_id   = "cloud0-labels"
  display_name = "cloud0 label writer"
}

# Project-level custom roles bind only in their own project. To label resources in other projects, create the same
# role there and grant it to this account (README → Assign products and labels).
resource "google_project_iam_member" "labels_writer" {
  project = var.project_id
  role    = google_project_iam_custom_role.label_writer.id
  member  = "serviceAccount:${google_service_account.labels.email}"
}
