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

- `PORT` — optional (default `8080`)
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
PORT=8080
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

## CI & Image
This repo uses a templated CI:
- On PRs, it validates: `gofmt`, `go vet`, `golangci-lint`, tests with race & coverage.
- On pushes to `master`, it builds and pushes a container image to Docker Hub.
