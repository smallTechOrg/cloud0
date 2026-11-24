output "workload_identity_provider" {
  description = "Value for the deploy workflow's WORKLOAD_IDENTITY_PROVIDER."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "artifact_registry" {
  description = "Docker repository host/path for cloud0 images."
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.images.repository_id}"
}

output "labels_service_account" {
  description = "Create a key for this account and store it with: deploy/provision.sh --labels-key <key.json>."
  value       = google_service_account.labels.email
}
