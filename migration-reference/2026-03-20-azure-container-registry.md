# Azure Container Registry (ACR) — Full Stack Integration

## Context

The project currently has no image registry. Production images are built locally and transferred via `docker save | ssh docker load` to the deploy server. CI builds all images from scratch on every run (~30 min). Agent sandbox snapshots are archived as diff-layer tarballs to Azure Blob Storage.

The goal is to introduce ACR as the central image store for build, CI, deploy, and optionally sandbox snapshots — decoupling image production from deployment and dramatically speeding up CI.

An earlier design doc (`design/2026-03-10-azure-container-registry.md`) scoped ACR for sandbox images only with Basic SKU (`platinumsandbox`). This plan expands scope to **all 6 service images** and uses Standard SKU.

## Decision: Keep Blob Storage for Agent Snapshots

**ACR is not a good fit for snapshots.** The current system extracts only filesystem diffs (KB–MB) and uploads a small gzipped tar to blob storage. Using ACR would require `docker push` of the full committed image (~3GB per snapshot, even though ACR deduplicates layers server-side the client still hashes all layers). Cost comparison:

| | Blob Storage (current) | ACR |
|---|---|---|
| 1000 snapshots @ 2MB avg | **$0.04/month** | **$20/month** (fixed) + push overhead |
| Transfer per snapshot | ~2MB upload | ~3GB layer hash + manifest |
| Complexity | Proven, simple | DinD auth, registry protocol |

**Verdict:** Snapshots stay on blob. ACR is for service images and sandbox base image only.

---

## Phase 1: Create ACR and Configure Authentication

### 1.1 Create the registry

```bash
# Use existing resource group (platinum-rg from design doc) or create one
az group create --name platinum-rg --location uksouth 2>/dev/null || true

az acr create \
  --resource-group platinum-rg \
  --name platinumacr \
  --sku Standard \
  --location uksouth
```

**SKU: Standard ($20/month)**
- 100GB included storage (Basic only has 10GB — too tight for 6 images × multiple tags)
- Supports retention policies and scheduled purge tasks
- Registry URL: `platinumacr.azurecr.io`

### 1.2 Image naming convention

```
platinumacr.azurecr.io/platinum/goapi:<hash>
platinumacr.azurecr.io/platinum/goworker:<hash>
platinumacr.azurecr.io/platinum/frontend:<hash>
platinumacr.azurecr.io/platinum/carbon:<hash>
platinumacr.azurecr.io/platinum/orchestrator:<hash>
platinumacr.azurecr.io/platinum/dind:<hash>
platinumacr.azurecr.io/platinum/sandbox:<tag>
```

### 1.3 Authentication — three contexts

**Developer workstation (Kai):**
```bash
az acr login --name platinumacr
# Uses existing Azure CLI credential. Token expires ~3 hours; re-run as needed.
```

**Deploy server (`carbon-worker-1`):**
```bash
# Create service principal with push+pull (for server-side builds)
az ad sp create-for-rbac \
  --name sp-platinum-acr \
  --role AcrPush \
  --scopes /subscriptions/$AZURE_SUBSCRIPTION_ID/resourceGroups/platinum-rg/providers/Microsoft.ContainerRegistry/registries/platinumacr

# One-time login on server (persists in ~/.docker/config.json)
docker login platinumacr.azurecr.io -u <sp-client-id> -p <sp-client-secret>
```

**GitHub Actions CI:**
- Add `ACR_REGISTRY`, `ACR_USERNAME`, `ACR_PASSWORD` to `.env` file
- Re-encode as `PLATINUM_ENV_BASE64` org secret (CI already decodes `.env`, so no new secrets needed)

### 1.4 New `.env` variables

```bash
ACR_REGISTRY=platinumacr.azurecr.io
ACR_USERNAME=<sp-client-id>      # Service principal for non-interactive login
ACR_PASSWORD=<sp-client-secret>  # Service principal secret
```

---

## Phase 2: Stack Script — Build and Push to ACR

### Files to modify
- `stack` — `build_images()`, `push_images()`, `deploy()`, new helper functions

### 2.1 Modify `build_images()` (line 564)

Add ACR tagging alongside local tags when `ACR_REGISTRY` is set:

```bash
function build_images() {
  local git_hash=$(git rev-parse --short=8 HEAD)
  local registry="${ACR_REGISTRY:-}"

  for spec in \
    "goapi:api-final-build:Dockerfile:." \
    "goworker:worker-final-build:Dockerfile:." \
    "frontend:frontend-final-build:Dockerfile:." \
    "carbon:carbon-final-build:Dockerfile:." \
    "orchestrator:orchestrator-final-build:Dockerfile:."; do

    IFS=: read -r name target dockerfile context <<< "$spec"
    local tags=("-t" "platinum-${name}:${git_hash}")
    if [[ -n "$registry" ]]; then
      tags+=("-t" "${registry}/platinum/${name}:${git_hash}")
    fi
    docker build --platform linux/amd64 \
      --build-arg APP_VERSION="$git_hash" \
      "${tags[@]}" --target "$target" -f "$dockerfile" "$context"
  done

  # DinD (different context)
  ensure_sandbox_build_context
  local dind_tags=("-t" "platinum-dind:${git_hash}")
  if [[ -n "$registry" ]]; then
    dind_tags+=("-t" "${registry}/platinum/dind:${git_hash}")
  fi
  docker build --platform linux/amd64 "${dind_tags[@]}" -f dind/Dockerfile.prod dind/
}
```

### 2.2 New `push_to_acr()` function

```bash
function push_to_acr() {
  local git_hash=$(git rev-parse --short=8 HEAD)
  local registry="${ACR_REGISTRY:?ACR_REGISTRY not set}"

  # Ensure logged in
  az acr login --name "${registry%%.*}" 2>/dev/null || \
    docker login "$registry" -u "$ACR_USERNAME" -p "$ACR_PASSWORD"

  for name in goapi goworker frontend carbon orchestrator dind; do
    local tag="${registry}/platinum/${name}:${git_hash}"
    echo "Pushing $tag..."
    docker push "$tag"
  done
}
```

### 2.3 Modify `push_images()` to be ACR-aware (line 619)

```bash
function push_images() {
  if [[ -n "${ACR_REGISTRY:-}" ]]; then
    push_to_acr
  else
    # Legacy SSH docker save/load path (unchanged)
    ...existing code...
  fi
}
```

### 2.4 New `pull_from_acr()` function

For deploy server to pull pre-built images:

```bash
function pull_from_acr() {
  local git_hash=$(git rev-parse --short=8 HEAD)
  local registry="${ACR_REGISTRY:?ACR_REGISTRY not set}"

  for name in goapi goworker frontend carbon orchestrator dind; do
    local tag="${registry}/platinum/${name}:${git_hash}"
    echo "Pulling $tag..."
    docker pull "$tag"
    # Also tag locally for compose compatibility
    docker tag "$tag" "platinum-${name}:${git_hash}"
  done
}
```

---

## Phase 3: Docker Compose — Registry-Aware Image References

### Files to modify
- `docker-compose.yml` — all 6 service image references

### Approach

Introduce `IMAGE_REPO_PREFIX` variable. The compose image references change from:

```yaml
image: platinum-goapi:${IMAGE_TAG:-latest}
```

to:

```yaml
image: ${IMAGE_REPO_PREFIX:-platinum-}goapi:${IMAGE_TAG:-latest}
```

- **Local dev** (unset): `platinum-goapi:latest` — no change
- **ACR deploy**: `IMAGE_REPO_PREFIX=platinumacr.azurecr.io/platinum/` → `platinumacr.azurecr.io/platinum/goapi:<hash>`

All 6 services get this treatment: carbon (line 53), goapi (line 67), goworker (line 118), frontend (line 145), dind (line 156), orchestrator (line 175).

**Note:** The router image (`binocarlos/noxy:v4`) and postgres image (`pgvector/pgvector:0.8.0-pg16`) stay unchanged — they're public images.

---

## Phase 4: Deploy Function — Pull from ACR

### Files to modify
- `stack` — `deploy()` function (line 648)

### 4.1 Modified deploy flow

The deploy function currently has 3 steps: build → push (SSH save/load) → start on server.

With ACR, the SSH step becomes a `pull`:

```
Step 1: build_images() — builds locally with ACR tags
Step 2: push_to_acr() — pushes to ACR (replaces SSH save/load)
Step 3: SSH to server → pull_from_acr() + docker compose up
```

The SSH heredoc (line 752) changes to:

```bash
ssh $DEPLOY_HOST <<EOF
set -euo pipefail
cd Platinum

# Pull from ACR (images already tagged locally by pull_from_acr)
export ACR_REGISTRY=$ACR_REGISTRY
source .env  # for ACR_USERNAME/ACR_PASSWORD if needed
./stack pull_from_acr

export ENV=$env_name IMAGE_TAG=$git_hash ...
docker compose -p $env_name down
docker volume create ${env_name}-dind-data 2>/dev/null || true
docker compose -p $env_name up -d
EOF
```

### 4.2 Server-side build option

New `deploy_remote` command for slow connections:

```bash
function deploy_remote() {
  local environment="${1:-}"
  local git_hash=$(git rev-parse --short=8 HEAD)
  # ... validation, confirmation ...

  ssh $DEPLOY_HOST <<EOF
    cd Platinum && git fetch origin && git checkout $git_hash
    ./stack build_images   # builds with ACR tags
    ./stack push_to_acr    # pushes to registry
    # Deploy using local images (already built)
    export ENV=$env_name IMAGE_TAG=$git_hash ...
    docker compose -p $env_name down && docker compose -p $env_name up -d
  EOF
}
```

---

## Phase 5: CI Integration

### Files to modify
- `.github/workflows/e2e-tests.yml` — both jobs

### 5.1 ACR login step (add to both e2e-fast and e2e-sandbox jobs)

```yaml
- name: Login to ACR
  run: |
    echo "$ACR_PASSWORD" | docker login "$ACR_LOGIN_SERVER" -u "$ACR_USERNAME" --password-stdin
  env:
    ACR_LOGIN_SERVER: ${{ env.ACR_REGISTRY }}  # from decoded .env
    ACR_USERNAME: ${{ env.ACR_USERNAME }}
    ACR_PASSWORD: ${{ env.ACR_PASSWORD }}
```

Since `.env` is already decoded, the ACR vars are available via `source .env` or can be read from the file.

### 5.2 Build cache from ACR (biggest CI speedup)

Use `docker buildx` with `--cache-from` pointing to ACR:

```yaml
- name: Set up Docker Buildx
  uses: docker/setup-buildx-action@v3

- name: Build with ACR cache
  run: |
    source .env
    for spec in goapi:api-dev-env goworker:api-worker-env frontend:ui-dev-env carbon:carbon-watch-env orchestrator:orchestrator-dev-env; do
      IFS=: read -r name target <<< "$spec"
      docker buildx build \
        --cache-from type=registry,ref=${ACR_REGISTRY}/platinum/${name}:buildcache \
        --cache-to type=registry,ref=${ACR_REGISTRY}/platinum/${name}:buildcache,mode=max \
        --target "$target" \
        -t "platinum-${name}:latest" \
        --load \
        -f Dockerfile .
    done
```

This caches Go module downloads, npm installs, .NET restores across CI runs. Expected speedup: 5-10 minutes (from ~30 min total).

### 5.3 Optional: Push production images after merge to main

```yaml
# New workflow or step in existing:
- name: Push to ACR
  if: github.event_name == 'push' && github.ref == 'refs/heads/main'
  run: |
    source .env
    HASH=${GITHUB_SHA::8}
    for name in goapi goworker frontend carbon orchestrator dind; do
      docker tag "platinum-${name}:latest" "${ACR_REGISTRY}/platinum/${name}:${HASH}"
      docker tag "platinum-${name}:latest" "${ACR_REGISTRY}/platinum/${name}:latest"
      docker push "${ACR_REGISTRY}/platinum/${name}:${HASH}"
      docker push "${ACR_REGISTRY}/platinum/${name}:latest"
    done
```

This sets up the path for future "merge → auto-deploy" workflows.

---

## Phase 6: DinD Sandbox Image via ACR

### Files to modify
- `dind/entrypoint-wrapper.sh` — add ACR pull before build
- `docker-compose.yml` — pass ACR env vars to DinD service

### 6.1 DinD ACR pull

Modify `entrypoint-wrapper.sh` to try pulling from ACR before building:

```bash
# If ACR is configured, try pulling pre-built sandbox image
if [ -n "${ACR_REGISTRY:-}" ]; then
  docker login "$ACR_REGISTRY" -u "$ACR_USERNAME" -p "$ACR_PASSWORD" 2>/dev/null || true
  if docker pull "${ACR_REGISTRY}/platinum/sandbox:latest" 2>/dev/null; then
    docker tag "${ACR_REGISTRY}/platinum/sandbox:latest" platinum-sandbox:latest
    echo "Pulled sandbox image from ACR"
  fi
fi
# Fall through to existing hash-check build logic
```

### 6.2 Push sandbox to ACR (new stack command)

```bash
function sandbox_push() {
  local tag="${1:-latest}"
  local registry="${ACR_REGISTRY:?ACR_REGISTRY not set}"
  # Execute inside DinD since that's where sandbox images live
  docker exec platinum-${ENV:-development}-dind \
    docker tag "platinum-sandbox:${tag}" "${registry}/platinum/sandbox:${tag}"
  docker exec platinum-${ENV:-development}-dind \
    docker push "${registry}/platinum/sandbox:${tag}"
}
```

### 6.3 Compose env passthrough

Add to `docker-compose.yml` DinD service (line 159):
```yaml
environment:
  - ACR_REGISTRY=${ACR_REGISTRY:-}
  - ACR_USERNAME=${ACR_USERNAME:-}
  - ACR_PASSWORD=${ACR_PASSWORD:-}
```

---

## Phase 7: Retention and Cleanup

### 7.1 ACR retention policy

```bash
# Purge untagged manifests after 7 days
az acr config retention update \
  --registry platinumacr \
  --status enabled \
  --days 7 \
  --type UntaggedManifests

# Scheduled purge: keep 20 most recent tags, delete rest older than 30 days
az acr task create --name purge-old-images \
  --registry platinumacr \
  --cmd "acr purge --filter 'platinum/goapi:.*' --ago 30d --keep 20 --untagged \
         && acr purge --filter 'platinum/goworker:.*' --ago 30d --keep 20 --untagged \
         && acr purge --filter 'platinum/frontend:.*' --ago 30d --keep 20 --untagged \
         && acr purge --filter 'platinum/carbon:.*' --ago 30d --keep 20 --untagged \
         && acr purge --filter 'platinum/orchestrator:.*' --ago 30d --keep 20 --untagged \
         && acr purge --filter 'platinum/dind:.*' --ago 30d --keep 20 --untagged" \
  --schedule "0 3 * * Sun" \
  --context /dev/null
```

### 7.2 Local cleanup command

Add `clean_images` to stack script:

```bash
function clean_images() {
  for name in platinum-goapi platinum-goworker platinum-frontend platinum-carbon platinum-orchestrator platinum-dind; do
    docker images "$name" --format '{{.Tag}}' | sort -r | tail -n +4 | while read tag; do
      docker rmi "$name:$tag" 2>/dev/null || true
    done
  done
}
```

---

## Implementation Order

| Step | Phase | What | Risk |
|------|-------|------|------|
| 1 | 1.1 | Create ACR via `az` CLI | Low |
| 2 | 1.3-1.4 | Create SP, add env vars | Low |
| 3 | 2.1-2.3 | Stack: build_images + push_to_acr | Medium |
| 4 | 3 | Docker compose IMAGE_REPO_PREFIX | Medium |
| 5 | 2.4 + 4.1 | Stack: pull_from_acr + deploy changes | Medium |
| 6 | 1.3 | One-time server setup (docker login) | Low |
| 7 | 5.1-5.2 | CI: ACR login + build cache | Medium |
| 8 | 6 | DinD: ACR pull for sandbox | High |
| 9 | 7 | Retention policies + cleanup | Low |
| 10 | 4.2 | Server-side build option | Low |

## Backward Compatibility

- `ACR_REGISTRY` unset → everything works exactly as today (local tags, SSH save/load)
- `IMAGE_REPO_PREFIX` unset → defaults to `platinum-` (current behavior)
- Legacy `push_images()` SSH path preserved behind conditional
- CI works without ACR (just slower, no cache)
- DinD falls through to local build if ACR pull fails

## Verification

1. **Phase 1:** `az acr show --name platinumacr` succeeds; `az acr login` + `docker pull` works
2. **Phase 2-3:** `./stack build_images && ./stack push_to_acr` succeeds; images visible in ACR
3. **Phase 4:** `./stack deploy staging` pulls from ACR and starts successfully
4. **Phase 5:** CI run shows cache hits in build log; total time drops
5. **Phase 6:** DinD pulls sandbox image from ACR on first startup (check `docker logs`)
6. **Phase 7:** `az acr repository show-tags` shows retention working
