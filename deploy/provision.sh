#!/usr/bin/env bash
# One-time, idempotent (check-then-create) setup of everything .github/workflows/deploy-cloudrun.yml needs. It never
# deploys: the first deploy is the next push (or Actions -> deploy-cloud-run -> "Run workflow").
# Run from the repo root as a project owner (gcloud auth login) with gh logged in to the repo:
#   bash deploy/provision.sh [--alerts-key /path/to/alerts-writer-key.json]
#
# Creates: the Artifact Registry repo `cloud0`, the bucket <project>-cloud0-data (Litestream replica of the SQLite
# file), the deployer account cloud0-deployer, and the Workload Identity provider `cloud0-github` (pool `github`) that
# lets this repo's push / workflow_dispatch runs impersonate it. The service runs as the existing read-only
# cloud0-reader (README section 10), which is only given object access to the data bucket.
#
# The repo is public, so identifiers go to GitHub repository secrets, never into the workflow: the project id and
# number, plus CLOUD0_GCP_PROJECTS, CLOUD0_BIGQUERY_PROJECT, CLOUD0_BILLING_EXPORT_TABLE and the optional
# CLOUD0_BILLING_ACCOUNT_ID / CLOUD0_OPENROUTER_API_KEY, read from the environment or ./.env (values are never printed).
# Cloud Run goes into CLOUD0_BIGQUERY_PROJECT unless PROJECT is set.
#
# --alerts-key stores the alerts-writer key in Secret Manager (cloud0-alerts-key) so Apply works on Cloud Run; without
# it the Alerts, Incidents and Budgets tabs are read-only there.
set -euo pipefail

GH_REPO="${GH_REPO:-smallTechOrg/cloud0}"
REGION="${REGION:-us-central1}"
AR_REPO=cloud0
WIF_POOL=github
WIF_PROVIDER=cloud0-github
ALERTS_SECRET=cloud0-alerts-key

ALERTS_KEY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --alerts-key) shift; ALERTS_KEY="${1:-}"; [ -f "$ALERTS_KEY" ] || { echo "--alerts-key needs an existing key file" >&2; exit 2; } ;;
    *) echo "usage: $0 [--alerts-key <path>]" >&2; exit 2 ;;
  esac
  shift
done

created() { echo "CREATED $1"; }
exists()  { echo "EXISTS  $1"; }
granted() { echo "GRANTED $1"; }
die()     { echo "ERROR   $1" >&2; exit 1; }
# New service accounts take a few seconds to become visible to IAM.
retry()   { for _ in 1 2 3 4 5 6; do "$@" >/dev/null 2>&1 && return 0; sleep 5; done; "$@" >/dev/null; }

# Value of an env var, else of the same key in ./.env.
setting() {
  local v="${!1:-}"
  if [ -z "$v" ] && [ -f .env ]; then
    v="$(grep -E "^$1=" .env | tail -1 | cut -d= -f2- | tr -d '\r' | sed -E "s/^['\"]//; s/['\"]$//" || true)"
  fi
  printf '%s' "$v"
}

[ -f go.mod ] && [ -d deploy ] || die "run from the repo root"
command -v gcloud >/dev/null || die "gcloud is not installed"
command -v gh >/dev/null || die "gh is not installed"
gcloud auth print-access-token >/dev/null 2>&1 || die "gcloud is not logged in: run gcloud auth login"
gh auth status >/dev/null 2>&1 || die "gh is not logged in: run gh auth login"
for k in CLOUD0_GCP_PROJECTS CLOUD0_BIGQUERY_PROJECT CLOUD0_BILLING_EXPORT_TABLE; do
  [ -n "$(setting "$k")" ] || die "$k is not set in the environment or .env"
done

PROJECT="${PROJECT:-$(setting CLOUD0_BIGQUERY_PROJECT)}"
PROJECT_NUMBER="$(gcloud projects describe "$PROJECT" --format='value(projectNumber)')"
G=(--project "$PROJECT")
RUN_SA="cloud0-reader@${PROJECT}.iam.gserviceaccount.com"
DEPLOY_SA="cloud0-deployer@${PROJECT}.iam.gserviceaccount.com"
BUCKET="${PROJECT}-cloud0-data"

echo "== APIs"
gcloud services enable run.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
  iamcredentials.googleapis.com sts.googleapis.com storage.googleapis.com secretmanager.googleapis.com \
  cloudresourcemanager.googleapis.com "${G[@]}"
echo "ENABLED run, artifactregistry, iam, iamcredentials, sts, storage, secretmanager, cloudresourcemanager"

echo "== Artifact Registry"
if gcloud artifacts repositories describe "$AR_REPO" --location "$REGION" "${G[@]}" >/dev/null 2>&1; then
  exists "repository $AR_REPO"
else
  gcloud artifacts repositories create "$AR_REPO" --repository-format docker --location "$REGION" \
    --description "cloud0 images" "${G[@]}"
  created "repository $AR_REPO"
fi

echo "== Data bucket (Litestream replica)"
if gcloud storage buckets describe "gs://$BUCKET" "${G[@]}" >/dev/null 2>&1; then
  exists "gs://$BUCKET"
else
  gcloud storage buckets create "gs://$BUCKET" --location "$REGION" --uniform-bucket-level-access \
    --public-access-prevention "${G[@]}"
  created "gs://$BUCKET"
fi

echo "== Service accounts"
gcloud iam service-accounts describe "$RUN_SA" "${G[@]}" >/dev/null 2>&1 \
  || die "$RUN_SA is missing: create the read-only account first (README section 10)"
exists "cloud0-reader (runtime)"
if gcloud iam service-accounts describe "$DEPLOY_SA" "${G[@]}" >/dev/null 2>&1; then
  exists "cloud0-deployer"
else
  gcloud iam service-accounts create cloud0-deployer --display-name "cloud0 GitHub deployer" "${G[@]}"
  created "cloud0-deployer"
fi

echo "== IAM"
retry gcloud projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:$DEPLOY_SA" \
  --role roles/run.admin --condition=None --quiet
granted "deployer: roles/run.admin on the project (deploy, and invoke for the health check)"
retry gcloud artifacts repositories add-iam-policy-binding "$AR_REPO" --location "$REGION" \
  --member "serviceAccount:$DEPLOY_SA" --role roles/artifactregistry.writer "${G[@]}"
granted "deployer: roles/artifactregistry.writer on $AR_REPO"
retry gcloud iam service-accounts add-iam-policy-binding "$RUN_SA" \
  --member "serviceAccount:$DEPLOY_SA" --role roles/iam.serviceAccountUser "${G[@]}"
granted "deployer: roles/iam.serviceAccountUser on cloud0-reader"
retry gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" \
  --member "serviceAccount:$RUN_SA" --role roles/storage.objectAdmin "${G[@]}"
granted "cloud0-reader: roles/storage.objectAdmin on gs://$BUCKET"

echo "== Workload Identity Federation"
if gcloud iam workload-identity-pools describe "$WIF_POOL" --location global "${G[@]}" >/dev/null 2>&1; then
  exists "pool $WIF_POOL"
else
  gcloud iam workload-identity-pools create "$WIF_POOL" --location global --display-name "GitHub Actions" "${G[@]}"
  created "pool $WIF_POOL"
fi
if gcloud iam workload-identity-pools providers describe "$WIF_PROVIDER" --location global \
     --workload-identity-pool "$WIF_POOL" "${G[@]}" >/dev/null 2>&1; then
  exists "provider $WIF_PROVIDER"
else
  gcloud iam workload-identity-pools providers create-oidc "$WIF_PROVIDER" --location global \
    --workload-identity-pool "$WIF_POOL" --display-name "cloud0 GitHub" \
    --issuer-uri https://token.actions.githubusercontent.com \
    --attribute-mapping "google.subject=assertion.sub,attribute.repository=assertion.repository" \
    --attribute-condition "assertion.repository=='${GH_REPO}' && (assertion.event_name=='push' || assertion.event_name=='workflow_dispatch')" \
    "${G[@]}"
  created "provider $WIF_PROVIDER (only $GH_REPO push / workflow_dispatch runs)"
fi
retry gcloud iam service-accounts add-iam-policy-binding "$DEPLOY_SA" --role roles/iam.workloadIdentityUser \
  --member "principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${WIF_POOL}/attribute.repository/${GH_REPO}" \
  "${G[@]}"
granted "$GH_REPO: roles/iam.workloadIdentityUser on cloud0-deployer"

if [ -n "$ALERTS_KEY" ]; then
  echo "== Alerts-writer key (Secret Manager)"
  if gcloud secrets describe "$ALERTS_SECRET" "${G[@]}" >/dev/null 2>&1; then
    exists "secret $ALERTS_SECRET"
  else
    gcloud secrets create "$ALERTS_SECRET" --replication-policy automatic "${G[@]}" >/dev/null
    created "secret $ALERTS_SECRET"
  fi
  if [ -n "$(gcloud secrets versions list "$ALERTS_SECRET" --filter=state=ENABLED --limit=1 --format='value(name)' "${G[@]}")" ]; then
    exists "an enabled version of $ALERTS_SECRET (rotate with: gcloud secrets versions add $ALERTS_SECRET --data-file=-)"
  else
    gcloud secrets versions add "$ALERTS_SECRET" --data-file=- "${G[@]}" < "$ALERTS_KEY" >/dev/null
    created "version of $ALERTS_SECRET"
  fi
  retry gcloud secrets add-iam-policy-binding "$ALERTS_SECRET" --member "serviceAccount:$RUN_SA" \
    --role roles/secretmanager.secretAccessor "${G[@]}"
  granted "cloud0-reader: roles/secretmanager.secretAccessor on $ALERTS_SECRET"
  retry gcloud secrets add-iam-policy-binding "$ALERTS_SECRET" --member "serviceAccount:$DEPLOY_SA" \
    --role roles/secretmanager.viewer "${G[@]}"
  granted "deployer: roles/secretmanager.viewer on $ALERTS_SECRET (to see that it exists)"
fi

echo "== GitHub repository secrets ($GH_REPO)"
put() {
  if [ -z "$2" ]; then echo "SKIPPED $1 (not set)"; return 0; fi
  printf '%s' "$2" | gh secret set "$1" -R "$GH_REPO"
  echo "SET     $1"
}
put GCP_PROJECT_ID "$PROJECT"
put GCP_PROJECT_NUMBER "$PROJECT_NUMBER"
for k in CLOUD0_GCP_PROJECTS CLOUD0_BIGQUERY_PROJECT CLOUD0_BILLING_EXPORT_TABLE CLOUD0_BILLING_ACCOUNT_ID CLOUD0_OPENROUTER_API_KEY; do
  put "$k" "$(setting "$k")"
done

echo
echo "Done. Deploy by pushing to main or the v0.1 feature branch, or: gh workflow run deploy-cloudrun.yml -R $GH_REPO --ref <branch>"
echo "Open the private service with: gcloud run services proxy cloud0 --region $REGION --project <PROJECT_ID>"
