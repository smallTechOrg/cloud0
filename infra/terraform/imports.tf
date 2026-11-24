# Adopt what deploy/provision.sh (and the README's manual steps) already created, instead of recreating it.
# After the first successful apply these blocks are no-ops and may stay; delete one only together with its resource.

import {
  for_each = toset(local.apis)
  to       = google_project_service.apis[each.value]
  id       = "${var.project_id}/${each.value}"
}

import {
  to = google_artifact_registry_repository.images
  id = "projects/${var.project_id}/locations/${var.region}/repositories/${var.artifact_repository}"
}

import {
  to = google_iam_workload_identity_pool_provider.github
  id = "projects/${var.project_id}/locations/global/workloadIdentityPools/${var.wif_pool_id}/providers/${var.wif_provider_id}"
}

import {
  to = google_service_account_iam_member.deployer_wif
  id = "projects/${var.project_id}/serviceAccounts/${local.deployer_sa_email} roles/iam.workloadIdentityUser principalSet://iam.googleapis.com/projects/${data.google_project.this.number}/locations/global/workloadIdentityPools/${var.wif_pool_id}/attribute.repository/${var.github_repo}"
}

import {
  to = google_project_iam_custom_role.label_writer
  id = "projects/${var.project_id}/roles/cloud0LabelWriter"
}

import {
  to = google_service_account.labels
  id = "projects/${var.project_id}/serviceAccounts/cloud0-labels@${var.project_id}.iam.gserviceaccount.com"
}

import {
  to = google_project_iam_member.labels_writer
  id = "${var.project_id} projects/${var.project_id}/roles/cloud0LabelWriter serviceAccount:cloud0-labels@${var.project_id}.iam.gserviceaccount.com"
}

import {
  to = google_secret_manager_secret.labels_key
  id = "projects/${var.project_id}/secrets/cloud0-labels-key"
}

import {
  to = google_secret_manager_secret_iam_member.labels_key_runtime
  id = "projects/${var.project_id}/secrets/cloud0-labels-key roles/secretmanager.secretAccessor serviceAccount:${local.runtime_sa_email}"
}
