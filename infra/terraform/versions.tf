terraform {
  required_version = ">= 1.6" # import blocks with variables (Terraform >= 1.6 / OpenTofu >= 1.6)

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }

  # State lives in a GCS bucket in the same project. Partial config: pass the bucket at init,
  #   terraform init -backend-config="bucket=<STATE_BUCKET>"
  # (see README.md → Bootstrap).
  backend "gcs" {
    prefix = "cloud0"
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}
