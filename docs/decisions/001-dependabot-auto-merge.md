# ADR-001: Auto-merge Dependabot minor/patch PRs with a GitHub App

## Status
Accepted

## Date
2026-09-11

## Context
Dependabot opens daily PRs for Go modules, Docker base images, GitHub Actions, and Terraform. Manual review of minor and patch bumps is slow and low-value. We still want humans to review major (and non-semver) updates, and we want CI to stay a merge gate.

Constraints:

- `GITHUB_TOKEN` reviews are attributed to `github-actions[bot]`. That identity is not a CODEOWNER.
- Official [CODEOWNERS](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners) syntax is users and teams only. GitHub App reviews land as `app-name[bot]` and do not count as code-owner approvals.
- This repository is personal (`ealebed/gh-app-cherry-pick-poc`), so organization ruleset bypass lists are not available.
- Dependabot-triggered `pull_request` workflows receive **Dependabot secrets only**, and `GITHUB_TOKEN` is read-only by default. [Source](https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-on-actions)
- The cherry-pick GitHub App runtime uses `GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY_PEM_BASE64`. Those are not the automerger credentials.
- [Container image CI](../../.github/workflows/wfl_image_ci.yaml) and [Terraform Plan](../../.github/workflows/terraform-plan.yml) are both path-filtered. Requiring both checks blocks every PR, because each ecosystem only reports one of them.

## Decision
Use a dedicated GitHub App (`automerger`) from a GitHub Actions workflow to approve and squash-auto-merge **semver-minor** and **semver-patch** Dependabot PRs.

Keep `.github/CODEOWNERS` so humans still get review requests. Do **not** enable “Require review from Code Owners”. Require **one** approving review plus required status check `ci / validate / Validate golang layer`. The workflow runs only when the PR author is `dependabot[bot]`.

Do **not** require `Terraform Plan` as a branch-protection check. Go/Docker PRs never report it. Terraform Dependabot PRs therefore stay manual: auto-merge may queue, but the golang check never reports, so GitHub will not squash-merge.

Widen Container image CI path filters to `.github/workflows/**` so GitHub Actions Dependabot PRs still run golang validate.

`gh pr merge --auto --squash` queues the merge. GitHub performs the squash only after required checks pass. Required checks must exist before this workflow is enabled, or a PR can merge with no CI.

Same pattern as `ealebed/token-injector`. Do not reuse the cherry-pick App (`ealebed-cherry-pick-poc`) for approve/merge.

## Alternatives Considered

### Fine-grained PAT of `@ealebed` (CODEOWNER)
- Pros: Approval would satisfy “Require review from Code Owners”.
- Cons: Long-lived credential tied to a person; revocation or expiry silently stops automation.
- Rejected: The App is the intended identity, and we accepted dropping the code-owner merge gate.

### `GITHUB_TOKEN` / `github-actions[bot]`
- Pros: No extra secrets.
- Cons: Does not satisfy code-owner reviews; still needs “Allow GitHub Actions to create and approve pull requests”; weaker attribution.
- Rejected: We want a dedicated App identity for approve/merge.

### Reuse the cherry-pick GitHub App
- Pros: Already installed on this repository.
- Cons: Different permissions and runtime secrets; mixing merge automation with webhook-driven cherry-picks.
- Rejected: `automerger` is the merge identity.

### Require both `ci / validate / Validate golang layer` and `Terraform Plan`
- Pros: Every ecosystem has a real CI gate.
- Cons: Classic required checks that never report block merge. Path filters mean most PRs only produce one of the two names.
- Rejected: Require the golang check only; merge terraform PRs by hand.

### Run golang CI on `terraform/**` so the required check always reports
- Pros: Terraform PRs could auto-merge.
- Cons: Go tests passing is not a Terraform gate; provider bumps could squash-merge without Terraform Plan.
- Rejected: Leave terraform path-filtered out of golang CI.

## Consequences
- Human PRs still request `@ealebed`; they are not auto-approved.
- Major and `semver-unknown` updates stay open for manual review (typical for some Docker tags).
- Terraform provider/module Dependabot PRs stay manual.
- `APP_CLIENT_ID` and `APP_PRIVATE_KEY` must exist in **both** Actions and Dependabot secret stores under identical names, and must not be the cherry-pick app key.
- Do not store an OAuth client secret; installation tokens need the App private key PEM.
- Client ID is read from secrets (not Actions variables) so Dependabot-triggered jobs can see it.
