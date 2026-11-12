#!/usr/bin/env bash
# One-time, idempotent (check-then-create) setup of everything .github/workflows/deploy-cloudrun.yml needs. It never
# deploys: the first deploy is the next push (or Actions -> deploy-cloud-run -> "Run workflow").
# Run from the repo root as a project owner (gcloud auth login) with gh logged in to the repo:
#   bash deploy/provision.sh [--alerts-key /path/to/alerts-writer-key.json]
#
# Creates: the Artifact Registry repo `cloud0`; on the shared Cloud SQL instance ai-agent-db (Postgres 17, private
# IP) the user cloud0_app and the database cloud0 it owns, whose connection URL becomes the CLOUD0_DATABASE_URL
# secret; and the Workload Identity provider `cloud0-github` (pool `github`) that lets this repo's push /
# workflow_dispatch runs impersonate the shared deployer github-actions-deployer. That account must already exist and
# already holds Cloud Run Admin, Artifact Registry writer and Service Account User on the project (it deploys the
# other services too), so nothing is granted to it here. The service runs as the existing read-only cloud0-reader
# (README section 10).
#
# The repo is public, so identifiers go to GitHub repository secrets, never into the workflow: the project id and
# number, plus CLOUD0_GCP_PROJECTS, CLOUD0_BIGQUERY_PROJECT, CLOUD0_BILLING_EXPORT_TABLE and the optional
# CLOUD0_BILLING_ACCOUNT_ID / CLOUD0_OPENROUTER_API_KEY, read from the environment or ./.env (values are never printed).
# CLOUD0_PASSWORD (required by the deploy: the URL is public) is read from the environment only, never .env, where it
# would also put local runs and the e2e suite behind the password:
#   CLOUD0_PASSWORD='<a long random password>' bash deploy/provision.sh     (or: gh secret set CLOUD0_PASSWORD)
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
SQL_INSTANCE="${SQL_INSTANCE:-ai-agent-db}"
DB_NAME=cloud0
DB_USER=cloud0_app
DBINIT_JOB=cloud0-dbinit
# The only branches allowed to deploy (keep in step with `on.push.branches` in deploy-cloudrun.yml).
DEPLOY_REFS="${DEPLOY_REFS:-refs/heads/main refs/heads/feature/cloud0-20261026-1851-v0.1}"

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
# put NAME VALUE sets a GitHub repository secret (value on stdin, never printed).
put() {
  if [ -z "$2" ]; then echo "SKIPPED $1 (not set)"; return 0; fi
  printf '%s' "$2" | gh secret set "$1" -R "$GH_REPO"
  echo "SET     $1"
}

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
# Git Bash on Windows: gcloud's launcher may pick a .bat Python shim (pyenv-win), which runs through cmd.exe and
# mangles arguments containing ( ) && || (formats, the WIF condition). Use the SDK's bundled Python instead.
if [ -z "${CLOUDSDK_PYTHON:-}" ]; then
  bundled="$(dirname "$(command -v gcloud)")/../platform/bundledpython/python.exe"
  if [ -x "$bundled" ]; then export CLOUDSDK_PYTHON="$bundled"; fi
fi
[ -n "$(gcloud info --format='value(basic.version)' 2>/dev/null)" ] || die "gcloud cannot print value() formats in this shell; set CLOUDSDK_PYTHON"
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
DEPLOY_SA="github-actions-deployer@${PROJECT}.iam.gserviceaccount.com"

echo "== APIs"
gcloud services enable run.googleapis.com artifactregistry.googleapis.com iam.googleapis.com \
  iamcredentials.googleapis.com sts.googleapis.com sqladmin.googleapis.com secretmanager.googleapis.com \
  cloudresourcemanager.googleapis.com "${G[@]}"
echo "ENABLED run, artifactregistry, iam, iamcredentials, sts, sqladmin, secretmanager, cloudresourcemanager"

echo "== Artifact Registry"
if gcloud artifacts repositories describe "$AR_REPO" --location "$REGION" "${G[@]}" >/dev/null 2>&1; then
  exists "repository $AR_REPO"
else
  gcloud artifacts repositories create "$AR_REPO" --repository-format docker --location "$REGION" \
    --description "cloud0 images" "${G[@]}"
  created "repository $AR_REPO"
fi

echo "== Service accounts"
gcloud iam service-accounts describe "$RUN_SA" "${G[@]}" >/dev/null 2>&1 \
  || die "$RUN_SA is missing: create the read-only account first (README section 10)"
exists "cloud0-reader (runtime)"
gcloud iam service-accounts describe "$DEPLOY_SA" "${G[@]}" >/dev/null 2>&1 \
  || die "$DEPLOY_SA is missing: it is the shared GitHub deployer and is not created here"
exists "github-actions-deployer (shared deployer)"

echo "== Cloud SQL ($SQL_INSTANCE)"
SQL_IP="${SQL_PRIVATE_IP:-}"
for i in 0 1 2 3; do
  [ -n "$SQL_IP" ] && break
  if [ "$(gcloud sql instances describe "$SQL_INSTANCE" "${G[@]}" --format="value(ipAddresses[$i].type)")" = PRIVATE ]; then
    SQL_IP="$(gcloud sql instances describe "$SQL_INSTANCE" "${G[@]}" --format="value(ipAddresses[$i].ipAddress)")"
  fi
done
[ -n "$SQL_IP" ] || die "$SQL_INSTANCE has no private IP (set SQL_PRIVATE_IP)"
DB_PASSWORD=""
if gcloud sql users list --instance "$SQL_INSTANCE" "${G[@]}" --format='value(name)' | grep -qx "$DB_USER"; then
  exists "sql-user $DB_USER (password not shown again; rotate: gcloud sql users set-password $DB_USER --instance $SQL_INSTANCE, then update the CLOUD0_DATABASE_URL secret)"
else
  # 32 random URL-safe base64 chars; the fixed tail meets Cloud SQL's policy (lower, upper, digit, symbol) every time.
  DB_PASSWORD="$(head -c 24 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=\n')Aa1-"
  # --password=… : the value may start with '-', which gcloud would read as a flag.
  gcloud sql users create "$DB_USER" --instance "$SQL_INSTANCE" --password="$DB_PASSWORD" "${G[@]}" >/dev/null
  created "sql-user $DB_USER"
  # Store the URL right away: the password exists nowhere else, and a later failure must not lose it. The copy in
  # .env.cloudrun (gitignored, not read by cloud0) sits next to CLOUD0_PASSWORD's.
  DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD}@${SQL_IP}:5432/${DB_NAME}?sslmode=require"
  printf 'CLOUD0_DATABASE_URL=%s\n' "$DATABASE_URL" >> .env.cloudrun
  echo "SAVED   CLOUD0_DATABASE_URL to .env.cloudrun"
  put CLOUD0_DATABASE_URL "$DATABASE_URL"
fi
if gcloud sql databases list --instance "$SQL_INSTANCE" "${G[@]}" --format='value(name)' | grep -qx "$DB_NAME"; then
  exists "database $DB_NAME"
else
  [ -n "$DB_PASSWORD" ] || die "database $DB_NAME is missing but $DB_USER already existed: rotate its password (above) and re-run, or create the database as $DB_USER"
  # The instance has only a private IP, so the database is created from inside the VPC by a one-off job, connected
  # as the app user, so that user owns it and its public schema (as workshop0's dbinit does). The job is deleted
  # afterwards because its environment holds the password.
  gcloud run jobs create "$DBINIT_JOB" --region "$REGION" --image docker.io/library/postgres:17-alpine \
    --command psql --args="-v,ON_ERROR_STOP=1,-c,CREATE DATABASE ${DB_NAME}" \
    --set-env-vars "PGHOST=${SQL_IP},PGUSER=${DB_USER},PGPASSWORD=${DB_PASSWORD},PGDATABASE=postgres,PGSSLMODE=require" \
    --service-account "$RUN_SA" --network default --subnet default --vpc-egress private-ranges-only \
    --max-retries 0 --task-timeout 120 "${G[@]}" --quiet >/dev/null
  if ! gcloud run jobs execute "$DBINIT_JOB" --region "$REGION" --wait "${G[@]}" >/dev/null; then
    gcloud run jobs delete "$DBINIT_JOB" --region "$REGION" --quiet "${G[@]}" >/dev/null || true
    die "creating database $DB_NAME failed; see the $DBINIT_JOB job logs in Cloud Logging"
  fi
  gcloud run jobs delete "$DBINIT_JOB" --region "$REGION" --quiet "${G[@]}" >/dev/null
  created "database $DB_NAME (owner $DB_USER)"
fi

echo "== Workload Identity Federation"
if gcloud iam workload-identity-pools describe "$WIF_POOL" --location global "${G[@]}" >/dev/null 2>&1; then
  exists "pool $WIF_POOL"
else
  gcloud iam workload-identity-pools create "$WIF_POOL" --location global --display-name "GitHub Actions" "${G[@]}"
  created "pool $WIF_POOL"
fi
# Only push / workflow_dispatch runs of this repo, on the deploy branches, may impersonate the shared deployer.
REFS_CEL="$(printf "'%s', " $DEPLOY_REFS)"
CONDITION="assertion.repository=='${GH_REPO}' && (assertion.event_name=='push' || assertion.event_name=='workflow_dispatch') && assertion.ref in [${REFS_CEL%, }]"
if gcloud iam workload-identity-pools providers describe "$WIF_PROVIDER" --location global \
     --workload-identity-pool "$WIF_POOL" "${G[@]}" >/dev/null 2>&1; then
  gcloud iam workload-identity-pools providers update-oidc "$WIF_PROVIDER" --location global \
    --workload-identity-pool "$WIF_POOL" --attribute-condition "$CONDITION" "${G[@]}" >/dev/null
  exists "provider $WIF_PROVIDER (condition set: $GH_REPO push / workflow_dispatch on $DEPLOY_REFS)"
else
  gcloud iam workload-identity-pools providers create-oidc "$WIF_PROVIDER" --location global \
    --workload-identity-pool "$WIF_POOL" --display-name "cloud0 GitHub" \
    --issuer-uri https://token.actions.githubusercontent.com \
    --attribute-mapping "google.subject=assertion.sub,attribute.repository=assertion.repository,attribute.ref=assertion.ref" \
    --attribute-condition "$CONDITION" \
    "${G[@]}"
  created "provider $WIF_PROVIDER ($GH_REPO push / workflow_dispatch on $DEPLOY_REFS)"
fi
retry gcloud iam service-accounts add-iam-policy-binding "$DEPLOY_SA" --role roles/iam.workloadIdentityUser \
  --member "principalSet://iam.googleapis.com/projects/${PROJECT_NUMBER}/locations/global/workloadIdentityPools/${WIF_POOL}/attribute.repository/${GH_REPO}" \
  "${G[@]}"
granted "$GH_REPO: roles/iam.workloadIdentityUser on github-actions-deployer"

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
fi

echo "== GitHub repository secrets ($GH_REPO)"
put GCP_PROJECT_ID "$PROJECT"
put GCP_PROJECT_NUMBER "$PROJECT_NUMBER"
for k in CLOUD0_GCP_PROJECTS CLOUD0_BIGQUERY_PROJECT CLOUD0_BILLING_EXPORT_TABLE CLOUD0_BILLING_ACCOUNT_ID CLOUD0_OPENROUTER_API_KEY; do
  put "$k" "$(setting "$k")"
done
put CLOUD0_PASSWORD "${CLOUD0_PASSWORD:-}"
# CLOUD0_DATABASE_URL was set above when the database user was created; it keeps its value on re-runs.
if [ -z "${CLOUD0_PASSWORD:-}" ] && ! gh secret list -R "$GH_REPO" | grep -q '^CLOUD0_PASSWORD\b'; then
  echo "NOTE    the deploy needs CLOUD0_PASSWORD: gh secret set CLOUD0_PASSWORD -R $GH_REPO"
fi

echo
echo "Done. Deploy by pushing to main or the v0.1 feature branch, or: gh workflow run deploy-cloudrun.yml -R $GH_REPO --ref <branch>"
echo "Then open the service URL and sign in with the CLOUD0_PASSWORD value."
