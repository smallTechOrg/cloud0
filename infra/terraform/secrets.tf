# Secret containers only. Key versions are added out of band (deploy/provision.sh --labels-key / --alerts-key, or
# gcloud secrets versions add) so private keys never enter Terraform state. The deploy workflow mounts a secret
# only when it has an enabled version.

resource "google_secret_manager_secret" "labels_key" {
  secret_id = "cloud0-labels-key"
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret_iam_member" "labels_key_runtime" {
  secret_id = google_secret_manager_secret.labels_key.id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${local.runtime_sa_email}"
}

resource "google_secret_manager_secret" "alerts_key" {
  count     = var.manage_alerts_secret ? 1 : 0
  secret_id = "cloud0-alerts-key"
  replication {
    auto {}
  }
  depends_on = [google_project_service.apis]
}

resource "google_secret_manager_secret_iam_member" "alerts_key_runtime" {
  count     = var.manage_alerts_secret ? 1 : 0
  secret_id = google_secret_manager_secret.alerts_key[0].id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${local.runtime_sa_email}"
}
