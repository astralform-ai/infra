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

`main` here mirrors `e2b-dev/runtime`; the fork exists to track upstream, not to run
upstream's CI. Our org carries none of e2b's credentials, and this repository is
public, so `[self-hosted, …]` jobs never schedule — use `ubuntu-latest`.

- Disabled on 2026-10-16, never runnable here: `Build and upload images`,
  `Promote envd`, `Release Please`.
- The generated-code check fails on every PR because this repo has no GitHub App
  secret; the failure is not about the PR's content.
- Our images are built by `astralform-ai/Astralform/.github/workflows/runner-image.yml`
  (`ubuntu-latest` → `ghcr.io/astralform-ai/agent-runtime` → `registry.astralform.ai`).

The full, verified facts live in `astralform-ai/Astralform` →
`.claude/skills/infrastructure/references/ci-runners.md` and `github-secrets.md`.
