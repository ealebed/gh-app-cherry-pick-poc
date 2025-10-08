# gh-app-cherry-pick-poc

[![Container image CI](https://github.com/ealebed/gh-app-cherry-pick-poc/actions/workflows/wfl_image_ci.yaml/badge.svg)](https://github.com/ealebed/gh-app-cherry-pick-poc/actions/workflows/wfl_image_ci.yaml)
[![Docker Pulls](https://img.shields.io/docker/pulls/ealebed/cherrypicker.svg)](https://hub.docker.com/r/ealebed/cherrypicker)
[![Docker Image Size (latest)](https://img.shields.io/docker/image-size/ealebed/cherrypicker/latest)](https://hub.docker.com/r/ealebed/cherrypicker/tags)

GitHub App that auto-creates cherry-pick PRs to release branches based on labels, for example: `cherry-pick to devops-release/0021`

---

## How it works

When a PR is **merged** (and also when labels are added to an already-merged PR):

1. The app scans labels matching: `^cherry-pick to (.+)$`.
2. For each target branch:
   - Verifies the branch exists.
   - Determines the merged commit SHA (works for merge/squash).
   - Creates a working branch from the target (e.g. `autocherry/devops-release-0021/<short-sha>`).
   - Runs `git cherry-pick -x <sha>` (uses `-m 1` for merge commits).
   - Pushes the work branch and opens a PR to the target branch.
   - Comments back on the source PR with the result (success link, no-op, conflicts, or branch missing).

Idempotent behaviors:
- If a work branch/PR for that target already exists, the app comments that it’s already open.
- If the cherry-pick is a no-op (commit already present / empty diff), it comments and skips opening a PR.

Was inspired with this [article](https://www.linkedin.com/blog/engineering/developer-experience-productivity/how-linkedin-automates-cherry-picking-commits-to-improve-develop).

---

## Prerequisites

### 1) Create & configure the GitHub App

- **Name**: `ealebed-cherry-pick-poc` (or any name you prefer).
- **Permissions** (Repository):
  - **Contents**: Read & write  
  - **Pull requests**: Read & write  
  - **Issues**: Read & write  
  - **Metadata**: Read
- **Webhook**:
  - **URL**: `https://<your-app-host>/webhook`
  - **Secret**: set a strong random value (you’ll reuse it as `GITHUB_WEBHOOK_SECRET`)
  - **Subscribe to events**: `pull_request`, `issue_comment`
- **Private key**: Generate and download the **PEM** for the app.

Install the app on the repositories where you want auto cherry-picks.

> **Branch protection**: The app never pushes directly to protected release branches. It only pushes a new **work branch** and opens a PR into the target.

### 2) Label format (what triggers the cherry-pick)

Add one or more labels to your PR (before or after merge): `cherry-pick to <target-branch>`

Examples:
- `cherry-pick to devops-release/0021`
- `cherry-pick to neo-release/0012`

If a labeled branch doesn’t exist, the app comments and skips that target.

### 3) Environment variables (for the server)

- `LISTEN_PORT` — optional (default `8080`)
- `LOG_LEVEL` - optional (default `info`)
- `GITHUB_APP_ID` — your GitHub App ID (integer)
- `GITHUB_WEBHOOK_SECRET` — the webhook secret you set in the app
- **Provide the app private key via one of:**
  - `GITHUB_APP_PRIVATE_KEY_PEM_BASE64` — **base64** of the PEM contents  
  - `GITHUB_APP_PRIVATE_KEY_PEM` — raw PEM contents (if you’ve wired it this way)

---

## Getting started (local dev)

### 0) Clone
```bash
git clone https://github.com/ealebed/gh-app-cherry-pick-poc.git
cd gh-app-cherry-pick-poc
```

### 1) Configure environment
Create `.env` (or export vars directly):
```bash
GITHUB_APP_ID=123456
GITHUB_WEBHOOK_SECRET=your-very-secret-string
GITHUB_APP_PRIVATE_KEY_PEM_BASE64=<<<paste base64 of your PEM here>>>
LISTEN_PORT=8080
LOG_LEVEL=debug
```

### 2) Run the server
```bash
go run ./cmd/server
```

### 3) Expose locally via ngrok
```bash
ngrok config add-authtoken <your-token>
ngrok http 8080

# Copy the https URL and set it in your GitHub App Webhook as: <url>/webhook
```

### 4) Try the flow
1) Open a PR to `master`.
2) Add a label, e.g. `cherry-pick to devops-release/0021`.
3) Merge the PR.
4) Check:
- A new work branch `autocherry/devops-release-0021/<short-sha>` should be pushed.
- A PR to `devops-release/0021` should be opened.
- A comment is added on the source PR (link to PR, or reason if skipped).

---

## CI & Image
This repo uses a templated CI:
- On PRs, it validates: `gofmt`, `go vet`, `golangci-lint`, tests with race & coverage.
- On pushes to `master`, it builds and pushes a container image to Docker Hub.

---

## Deploying to Google Cloud Run

This service receives GitHub webhooks, verifies the HMAC signature, and then calls the GitHub API. On Cloud Run we’ll:

* deploy the container,
* wire up secrets via **Secret Manager** (webhook secret + app private key PEM **in base64**),
* expose the service publicly so GitHub can reach it,
* set a small min instance to avoid cold starts on webhooks.

### Prerequisites

* A GCP project with **billing enabled** and the **gcloud** CLI authenticated:

  ```bash
  gcloud auth login
  gcloud config set project <PROJECT_ID>
  ```
* Enable required APIs:

  ```bash
  gcloud services enable run.googleapis.com secretmanager.googleapis.com
  ```
* Your container image in Docker Hub (public), e.g. `docker.io/ealebed/cherrypicker:latest`.

### 1) Create secrets in Secret Manager

**We’ll store:**

* `GITHUB_WEBHOOK_SECRET` as plain text.
* `GITHUB_APP_PRIVATE_KEY_PEM_BASE64` as the **base64** contents of your GitHub App private key PEM (because the app expects base64).

> Secret Manager commands below create a secret and add version 1 from stdin

```bash
# Replace with real values
WEBHOOK_SECRET="super-strong-random-string"

# (macOS) Base64 for PEM in one line:
#   base64 -i /path/to/private-key.pem | tr -d '\n' > /tmp/appkey.b64
# (Linux):
#   base64 -w0 < /path/to/private-key.pem > /tmp/appkey.b64

# 1) Webhook secret
echo -n "${WEBHOOK_SECRET}" | gcloud secrets create gh-webhook-secret --data-file=-

# 2) GitHub App private key (BASE64 STRING)
# If you created /tmp/appkey.b64 as above:
gcloud secrets create gh-app-private-key-b64 --data-file=/tmp/appkey.b64
```

## 2) (Recommended) Create a dedicated service account

Gives the service least privileges + access to read secrets:

```bash
gcloud iam service-accounts create cherry-bot --display-name="Cherry-pick Bot"

# Allow reading secrets
gcloud projects add-iam-policy-binding $(gcloud config get-value project) \
  --member="serviceAccount:cherry-bot@$(gcloud config get-value project).iam.gserviceaccount.com" \
  --role="roles/secretmanager.secretAccessor"
```

## 3) Deploy to Cloud Run

Choose a region close to you (e.g., `europe-central2`). Set variables:

```bash
PROJECT_ID=$(gcloud config get-value project)
REGION="europe-central2"          # pick your region
SERVICE="cherrypicker"            # Cloud Run service name
IMAGE="docker.io/ealebed/cherrypicker:latest"  # or a timestamp tag from your CI
SA="cherry-bot@${PROJECT_ID}.iam.gserviceaccount.com"
```

Deploy:

```bash
gcloud run deploy "${SERVICE}" \
  --image="${IMAGE}" \
  --region="${REGION}" \
  --service-account="${SA}" \
  --allow-unauthenticated \
  --cpu=1 --memory=256Mi \
  --min-instances=1 --max-instances=5 \
  --port=8080 \
  --set-env-vars="LISTEN_PORT=:8080,GITHUB_APP_ID=<YOUR_APP_ID>,GIT_USER_NAME=cherry-pick-bot,GIT_USER_EMAIL=cherry-pick-bot@users.noreply.github.com,LOG_LEVEL=info" \
  --set-secrets="GITHUB_WEBHOOK_SECRET=gh-webhook-secret:latest,GITHUB_APP_PRIVATE_KEY_PEM_BASE64=gh-app-private-key-b64:latest"
```

**Notes:**

* `--allow-unauthenticated` makes the service publicly callable (signature verification still protects you). If your org policy forbids public services, you’ll need to change that or front with an alternative—but for GitHub webhooks, public access is standard.
* `--min-instances=1` avoids cold start delays so GitHub doesn’t retry webhooks. Tune `max-instances` to your traffic/budget. 

## 4) Set the GitHub App webhook URL

After deploy, get the URL:

```bash
URL=$(gcloud run services describe "${SERVICE}" --region "${REGION}" --format="value(status.url)")
echo "${URL}"
```

In your GitHub App settings, set the webhook to:

```
<URL>/webhook
```

(For example: `https://cherrypicker-abcde-uc.a.run.app/webhook`)

## 5) Test the flow

## 6) Updating the service

When CI pushes a new image tag:

```bash
NEW_TAG="2025.10.08-12.00"
gcloud run deploy "${SERVICE}" \
  --image="docker.io/ealebed/cherrypicker:${NEW_TAG}" \
  --region="${REGION}"
```

(Env vars and secrets persist across revisions unless you change them.)

---
