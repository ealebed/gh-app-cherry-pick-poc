# gh-app-cherry-pick-poc

LinkedIn-style GitHub App that auto-creates cherry-pick PRs to protected release branches based on labels:
- `cherry-pick to devops-release/0021`

## How it works
1. On merged PRs, the App looks for labels matching `^cherry-pick to (.+)$`.
2. For each target branch:
   - verifies branch exists,
   - finds the merged commit OID (works for merge/squash via GraphQL MergedEvent),
   - creates a working branch from the target,
   - `git cherry-pick -x <sha>`,
   - pushes and opens a PR to the target branch,
   - comments back on the source PR.

## Configure the GitHub App
- In the App settings, set **Permissions**:
  - Contents: Read & write
  - Pull requests: Read & write
  - Issues: Read & write
  - Metadata: Read
- Install the App on your test repo.

## Local dev (with a webhook proxy)
```bash
cp .env.example .env
# Fill IDs/secrets, base64 the PEM
go run ./cmd/server
