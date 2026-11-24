variable "project_id" {
  description = "GCP project that hosts cloud0 (Cloud Run, Artifact Registry, secrets, service accounts)."
  type        = string
}

variable "region" {
  description = "Region of Cloud Run and Artifact Registry."
  type        = string
  default     = "us-central1"
}

variable "github_repo" {
  description = "owner/name of the repository whose GitHub Actions deploy cloud0."
  type        = string
}

variable "deploy_refs" {
  description = "Git refs allowed to deploy (keep in step with on.push.branches in .github/workflows/deploy-cloudrun.yml)."
  type        = list(string)
  default     = ["refs/heads/main"]
}

variable "wif_pool_id" {
  description = "Workload Identity pool for GitHub Actions. It may be shared with other repositories, so it is referenced, never managed, here."
  type        = string
  default     = "github"
}

variable "wif_provider_id" {
  description = "cloud0's own OIDC provider inside the pool."
  type        = string
  default     = "cloud0-github"
}

variable "deployer_service_account" {
  description = "Account id (before @) of the GitHub deployer service account. Often shared, so only cloud0's binding on it is managed."
  type        = string
  default     = "github-actions-deployer"
}

variable "runtime_service_account" {
  description = "Account id of the read-only account the Cloud Run service runs as (created per README section 10)."
  type        = string
  default     = "cloud0-reader"
}

variable "artifact_repository" {
  description = "Artifact Registry Docker repository for cloud0 images."
  type        = string
  default     = "cloud0"
}

variable "manage_alerts_secret" {
  description = "Create the cloud0-alerts-key secret container (its key version is added out of band, never through Terraform)."
  type        = bool
  default     = false
}
