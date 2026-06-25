#!/usr/bin/env bash
#
# Idempotent GCP provisioning for the Agent Orange stack. Safe to re-run: every
# step checks first and only creates what's missing. Run it once per project (or
# any time you spin up a fresh project) with an account that has admin rights.
#
# What it ensures:
#   - the Artifact Registry + Cloud Storage APIs are enabled
#   - a Docker Artifact Registry repo exists           (images / session snapshots)
#   - a GCS bucket exists                               (artifact + snapshot bytes)
#   - a runtime service account exists                 (agentd's identity)
#   - that SA has least-privilege IAM:
#       roles/storage.objectAdmin     on the bucket    (read/write/delete/list objects)
#       roles/artifactregistry.writer on the repo      (push + pull)
#
# It does NOT create a key by default (keys are long-lived secrets). Pass
# --emit-key <path> to write one for local/CI use; on GCP infra prefer workload
# identity and skip the key entirely.
#
# Usage:
#   deploy/gcp/setup.sh [--emit-key ./gcp-key.json]
#
# Override any of these via env (defaults match the project's resources):
#   PROJECT REGION GCS_BUCKET GCP_AR_REPO SA_NAME

set -euo pipefail

PROJECT="${PROJECT:-webkit-servers}"
REGION="${REGION:-europe-west1}"
BUCKET="${GCS_BUCKET:-webkit-servers-agent-orange}"
AR_REPO="${GCP_AR_REPO:-agent-orange}"
SA_NAME="${SA_NAME:-agent-orange-runtime}"
SA_EMAIL="${SA_NAME}@${PROJECT}.iam.gserviceaccount.com"

EMIT_KEY=""
while [ $# -gt 0 ]; do
  case "$1" in
    --emit-key) EMIT_KEY="${2:?--emit-key needs a path}"; shift 2 ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

log "Project=$PROJECT region=$REGION bucket=$BUCKET repo=$AR_REPO sa=$SA_EMAIL"

log "Enabling APIs (idempotent)"
gcloud services enable artifactregistry.googleapis.com storage.googleapis.com \
  --project="$PROJECT" >/dev/null

log "Artifact Registry repo"
if gcloud artifacts repositories describe "$AR_REPO" \
     --location="$REGION" --project="$PROJECT" >/dev/null 2>&1; then
  echo "    exists: $AR_REPO"
else
  gcloud artifacts repositories create "$AR_REPO" \
    --repository-format=docker --location="$REGION" \
    --description="Agent Orange session/base images" --project="$PROJECT"
fi

log "GCS bucket"
if gcloud storage buckets describe "gs://$BUCKET" --project="$PROJECT" >/dev/null 2>&1; then
  echo "    exists: gs://$BUCKET"
else
  gcloud storage buckets create "gs://$BUCKET" \
    --location="$REGION" --uniform-bucket-level-access --project="$PROJECT"
fi

log "Service account"
if gcloud iam service-accounts describe "$SA_EMAIL" --project="$PROJECT" >/dev/null 2>&1; then
  echo "    exists: $SA_EMAIL"
else
  gcloud iam service-accounts create "$SA_NAME" \
    --display-name="Agent Orange runtime" --project="$PROJECT"
fi

# add-iam-policy-binding is idempotent: re-adding an existing binding is a no-op.
log "IAM: storage.objectAdmin on gs://$BUCKET"
gcloud storage buckets add-iam-policy-binding "gs://$BUCKET" \
  --member="serviceAccount:$SA_EMAIL" --role="roles/storage.objectAdmin" \
  --project="$PROJECT" >/dev/null

log "IAM: artifactregistry.writer on $AR_REPO"
gcloud artifacts repositories add-iam-policy-binding "$AR_REPO" \
  --location="$REGION" --member="serviceAccount:$SA_EMAIL" \
  --role="roles/artifactregistry.writer" --project="$PROJECT" >/dev/null

if [ -n "$EMIT_KEY" ]; then
  if [ -f "$EMIT_KEY" ]; then
    log "Key already present at $EMIT_KEY (not overwriting)"
  else
    log "Writing service-account key → $EMIT_KEY"
    gcloud iam service-accounts keys create "$EMIT_KEY" \
      --iam-account="$SA_EMAIL" --project="$PROJECT"
    echo "    set GOOGLE_APPLICATION_CREDENTIALS=$EMIT_KEY for agentd"
  fi
fi

log "Done. Registry: ${REGION}-docker.pkg.dev/${PROJECT}/${AR_REPO}  Bucket: gs://${BUCKET}"
