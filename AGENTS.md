# AGENTS.md

Instructions for AI agents working in this repository.

## Understanding the repository

Read [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) before working on or reviewing code here. It describes what each service does, how services interact (sandbox creation, traffic routing, pause/resume, template builds), the data stores, and the deployment topology — it is the fastest way to build correct context about this codebase.

Keep that document updated: if a code change alters anything it describes (service responsibilities, ports, protocols, data stores, flows, deployment topology), update `docs/ARCHITECTURE.md` as part of the same change. When reviewing a PR that changes such details without updating the document, flag it.

## Review guidelines (pull requests)

Output style rules (strict, override any defaults):

- Only report concrete bugs, regressions, or correctness issues. Do not summarize the PR, the diff, or what changed.
- One short paragraph per finding. No preamble, no closing remark.
- No headers, no bullet lists, no tables, no diagrams.
- No emojis. No severity tags ("bug", "nit", "suggestion", "enhancement"). No labels.
- No branding or footer lines.
- If there are no real issues to flag, post no review at all.

## Scope

- Skip style/nit comments — `golangci-lint` covers those.
- Skip test-coverage comments — Codecov covers those.
- Focus on: race conditions, nil-deref, error handling, auth/authz, request routing, resource leaks, SQL/migration correctness, and gRPC/proto compatibility.

## Fork-local: this repository inside `astralform-ai`

`main` mirrors `e2b-dev/runtime` and carries only a handful of local commits; the fork
tracks upstream rather than running upstream's CI. Our org carries none of e2b's
credentials, and this repository is public, so `[self-hosted, …]` jobs never schedule —
use a GitHub-hosted runner (`ubuntu-latest` / `ubuntu-24.04`).

- Disabled on 2026-10-16: `Build and upload images` and `Promote envd`, both gated on an
  Infisical identity that exists at no scope here — they skip rather than fail, and a
  `Promote envd` dispatch against an unprovisioned repo concludes green and promotes
  nothing — and `Release Please`, which runs on the default token and fails at PR
  creation (`GitHub Actions is not permitted to create or approve pull requests`, with
  no `vars.RELEASE_PLEASE_APP_ID`).
- The generated-code check skips unless `vars.AUTOFIXER_APP_ID` is set: the reusable
  workflow mints e2b's GitHub App token, and this org carries no such secret. Set both
  and it runs; until then a PR touching generated inputs stays green rather than failing
  on `[@octokit/auth-app] appId option is required`.
- Our images are built by `astralform-ai/Astralform/.github/workflows/runner-image.yml`
  (`ubuntu-latest` → `ghcr.io/astralform-ai/agent-runtime`), whose `mirror` job then pushes
  them into `registry.astralform.ai` on `[self-hosted, hostinger]`.

The full, verified facts live in `astralform-ai/Astralform` →
`.claude/skills/infrastructure/references/ci-runners.md` and `github-secrets.md`.
