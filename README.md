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

3. Auto-create label when a new release branch is created (pattern: `<team>-release/NNNN` leads to creation label `cherry-pick to <branch>`).
4. Retention: keep only the latest 5 labels per team and delete older ones.
5. Repo label cascade deletion: when we delete labels (as part of retention), we’ll first remove them from PRs; users deleting labels in GitHub UI are already handled by GitHub (labels disappear from PRs).
6. Unlabel on "initial" PR leads to retracting autocherry PR: removing a `cherry-pick to ...` label closes the corresponding child cherry-pick PR (if open) and deletes the work branch.

Was inspired with this [article](https://www.linkedin.com/blog/engineering/developer-experience-productivity/how-linkedin-automates-cherry-picking-commits-to-improve-develop).

---

## Prerequisites

### 1) Create & configure the GitHub App

- **Name**: `ealebed-cherry-pick-poc` (or any name you prefer).
- **Permissions** (Repository):
  - **Contents**: Read & write (push branches, delete refs)
  - **Pull requests**: Read & write (open/close PRs, comment)
  - **Issues**: Read & write (create/delete labels, add/remove labels on PRs)
  - **Metadata**: Read (default)
- **Webhook**:
  - **URL**: `https://<your-app-host>/webhook`
  - **Secret**: set a strong random value (you’ll reuse it as `GITHUB_WEBHOOK_SECRET`)
  - **Subscribe to events**:
    - `pull_request` (Pull request assigned, auto merge disabled, auto merge enabled, closed, converted to draft, demilestoned, dequeued, edited, enqueued, labeled, locked, milestoned, opened, ready for review, reopened, review request removed, review requested, synchronized, unassigned, unlabeled, or unlocked)
    - `issue_comment` (Issue comment created, edited, or deleted)
    - `create` (Branch or tag created)
- **Private key**: Generate and download the **PEM** for the app.

Install the app on the repositories where you want auto cherry-picks.

> **Branch protection**: The app never pushes directly to protected release branches. It only pushes a new **work branch** and opens a PR into the target.

### 2) Label format (what triggers the cherry-pick)

Add one or more labels to your PR (before or after merge): `cherry-pick to <target-branch>`

Examples:
- `cherry-pick to devops-release/0021`
- `cherry-pick to neo-release/0012`

If a labeled branch doesn’t exist, the app comments and skips that target.

### 3) Environment variables (for the application)

- `LISTEN_PORT` — optional (default `:8080`)
- `LOG_LEVEL` - optional (default `info`)
- `SQS_QUEUE_URL` - еhe full URL of the main SQS queue the worker will poll
- `SQS_MAX_MESSAGES` - optional (default `10`)
- `SQS_WAIT_TIME_SECONDS` - optional (default `10`)
- `SQS_VISIBILITY_TIMEOUT` - optional (default `120`)
- `SQS_DELETE_ON_4XX` - optional (default `true`)
- `AWS_REGION` - optional (default `eu-north-1`)
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
LISTEN_PORT=:8080
LOG_LEVEL=debug
AWS_REGION=eu-north-1
SQS_QUEUE_URL=https://sqs.eu-north-1.amazonaws.com/531438381462/ghapp-poc-queue
SQS_MAX_MESSAGES=10
SQS_WAIT_TIME_SECONDS=10
SQS_VISIBILITY_TIMEOUT=120
SQS_DELETE_ON_4XX=true
```

### 2) Run the application
```bash
go run ./cmd/server
```

> Health check is at `GET /healthz`. The worker consumes from `SQS_QUEUE_URL`.


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

- deploy the container,
- wire up secrets via **Secret Manager** (webhook secret + app private key PEM **in base64**),
- expose the service publicly so GitHub can reach it,
- set a small min instance to avoid cold starts on webhooks.

### Prerequisites

- A GCP project with **billing enabled** and the **gcloud** CLI authenticated:

  ```bash
  gcloud auth login
  gcloud config set project <PROJECT_ID>
  ```
- Enable required APIs:

  ```bash
  gcloud services enable run.googleapis.com secretmanager.googleapis.com
  ```
- Your container image in Docker Hub (public), e.g. `docker.io/ealebed/cherrypicker:latest`.

### 1) Create secrets in Secret Manager

**We’ll store:**

- `GITHUB_WEBHOOK_SECRET` as plain text.
- `GITHUB_APP_PRIVATE_KEY_PEM_BASE64` as the **base64** contents of your GitHub App private key PEM (because the app expects base64).

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

### 2) (Recommended) Create a dedicated service account

Gives the service least privileges + access to read secrets:

```bash
gcloud iam service-accounts create cherry-bot --display-name="Cherry-pick Bot"

# Allow reading secrets
gcloud projects add-iam-policy-binding $(gcloud config get-value project) \
  --member="serviceAccount:cherry-bot@$(gcloud config get-value project).iam.gserviceaccount.com" \
  --role="roles/secretmanager.secretAccessor"
```

### 3) Deploy to Cloud Run

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

- `--allow-unauthenticated` makes the service publicly callable (signature verification still protects you). If your org policy forbids public services, you’ll need to change that or front with an alternative—but for GitHub webhooks, public access is standard.
- `--min-instances=1` avoids cold start delays so GitHub doesn’t retry webhooks. Tune `max-instances` to your traffic/budget.

### 4) Set the GitHub App webhook URL

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

### 5) Test the flow

### 6) Updating the service

When CI pushes a new image tag:

```bash
NEW_TAG="2025.10.08-12.00"
gcloud run deploy "${SERVICE}" \
  --image="docker.io/ealebed/cherrypicker:${NEW_TAG}" \
  --region="${REGION}"
```

(Env vars and secrets persist across revisions unless you change them.)

---

## Deploying to Kubernetes

### Prerequisites

- A Kubernetes cluster with an external LoadBalancer (e.g., GKE/EKS/AKS).
- `kubectl` pointing at your cluster context.
- Your container image published to Docker Hub, e.g. `docker.io/ealebed/cherrypicker:<tag>`.

### 1) Create namespace
```bash
kubectl create namespace cherrypicker
```

### 2) Create secrets
The app expects:
- `GITHUB_WEBHOOK_SECRET` – webhook secret string.
- `GITHUB_APP_PRIVATE_KEY_PEM_BASE64` – base64 of your GitHub App private key PEM.

Webhook secret:
```bash
kubectl -n cherrypicker create secret generic gh-webhook-secret \
  --from-literal=value='your-super-strong-webhook-secret'
```

Private key (base64 of PEM):
```bash
kubectl -n cherrypicker create secret generic gh-app-private-key-b64 \
  --from-literal=value="$(cat /tmp/appkey.b64)"
```
Tip: The value stored in the secret is the base64 string itself (single line, no trailing newline).

### 3) Apply the manifest
Edit the YAML to set your real `GITHUB_APP_ID` and preferred image tag, then:
```bash
kubectl apply -f k8s/cherrypicker.yaml
```

### 4) Get the external address
```bash
# Watch until an external IP/hostname appears
kubectl -n cherrypicker get svc cherrypicker --watch
```
Copy the external address and configure your GitHub App webhook URL as:
```bash
http(s)://<external-address>/webhook
```
> **HTTPS recommended:** If your LB doesn’t terminate TLS, add an Ingress (e.g., NGINX + cert-manager) to get an HTTPS hostname and point the webhook there.

### 5) Test the flow

---

## Deploying to AWS (API Gateway → SQS → ECS/Fargate)

This repository includes Terraform to provision:
- API Gateway (REST) endpoint /webhook that forwards GitHub payloads to SQS.
- SQS main queue (+ DLQ) for decoupling and retries.
- ECS Fargate service that runs the worker (SQS poller), with logs to CloudWatch.
- IAM roles/policies for API Gateway → SQS, and ECS task permissions.

### Prerequisites

- AWS account + credentials exported as env vars:
```bash
export AWS_ACCESS_KEY_ID=...
export AWS_SECRET_ACCESS_KEY=...
export AWS_REGION=eu-north-1
```

- Terraform `>= 1.6`, AWS provider `~> 5.0`.
- Your container image (see `variables.tf` `worker_image`). Defaults to: `docker.io/ealebed/cherrypicker:2025.10.10-11.50`

### 1) Create Secrets in AWS Secrets Manager

We store:
- `GITHUB_WEBHOOK_SECRET` — plain string
- `GITHUB_APP_PRIVATE_KEY_PEM_BASE64` — base64 of your GitHub App PEM

Example (names match Terraform data sources):
```bash
# Webhook secret (plain)
aws secretsmanager create-secret \
  --name ghapp/webhook_secret \
  --secret-string 'your-webhook-secret-value'

# PEM -> base64 (single-line). On macOS:
base64 -i /path/to/app-private-key.pem | tr -d '\n' > /tmp/appkey.b64

# Private key (base64 string)
aws secretsmanager create-secret \
  --name ghapp/private_key_pem_b64 \
  --secret-string "$(cat /tmp/appkey.b64)"
```

> Terraform reads these with data sources (`data "aws_secretsmanager_secret" ...`) and grants the ECS execution role `secretsmanager:GetSecretValue` to inject them into the container environment.

### 2) Review / set the image tag (optional)
Edit variables.tf if you want a different image:
```bash
variable "worker_image" {
  default = "docker.io/youruser/yourimage:yourtag"
}
```

### 3) Provision infra with Terraform

From the `terraform` repo folder:
```bash
terraform init
terraform apply -auto-approve
```

Key outputs:
- `webhook_url` — Use this as the GitHub App Webhook URL
Example: `https://{api-id}.execute-api.{region}.amazonaws.com/dev/webhook`
- `sqs_queue_url` — The worker reads from this queue

### 4) Configure your GitHub App webhook
In your GitHub App settings:

- Set Webhook URL to the Terraform output `webhook_url`.
- Set the secret to the same value you stored in Secrets Manager (`ghapp/webhook_secret`).

### 5) Verify the ECS worker is healthy

- ECS Service: `ghapp-poc-worker`
- Health check hits `GET /healthz` (we use `curl` in the task definition).
- Logs are in CloudWatch Logs group `/ecs/ghapp-poc-worker`

### 6) Test the flow end-to-end

---

## TODO:
- Test coverage report/badge
- Expose `/metrics` with counters for events processed, PRs opened, conflicts, no-ops
