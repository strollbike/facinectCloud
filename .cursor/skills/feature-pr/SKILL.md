---
name: feature-pr
description: >-
  Feature-wise git branch, push, and GitHub PR for any Facinect microservice or
  app (identity, notifications, web, gateway, and future services). Use when the
  user asks to ship a feature, create a PR, push service changes, open a pull
  request, feature branch, or feature-wise code push. Always test, write a
  report, and wait for user approval before any push.
---

# Feature-wise branch + PR (all services)

Monorepo workflow for **every** service/app — not notifications-only.

## Scope

| Area | Paths |
|------|--------|
| Services | `services/*` (identity, notifications, future `booking`, etc.) |
| Apps | `apps/*` (web, …) |
| Platform | `gateway/`, `docker-compose*.yml`, `docs/`, `scripts/`, `sql/` |

Never commit secrets: `.env`, `.env.prod`, keys, tokens, credential dumps.

## Triggers

`ship feature`, `feature PR`, `push this service`, `create PR`, `feature branch`, `feature-wise push`.

## Hard gate (mandatory)

**Do not `git push` and do not `gh pr create` until the user explicitly approves.**

Order every ship:

1. Implement / stage / commit locally on `feat/<service>-<slug>` (commit only if user asked to ship/PR/commit).
2. **Test** the changed services (e.g. `go test` in Docker for Go services; Flutter analyze for StrollX when app changed).
3. **Report document** in the chat (and optionally `docs/` only if the user asked for a file). Include:
   - What changed (paths + why)
   - Test commands + pass/fail
   - Risks / Meta or env notes
   - Suggested VPS rebuild commands (do not run unless asked)
4. **Wait** for approval words such as: `approve`, `approved`, `ok push`, `push`, `go ahead`.
5. Only then: push + `gh pr create` + return PR URL.

If the user has not approved yet, stop after the report. Do not push “to save time.”

## Workflow

1. **Inspect** (parallel): `git status`, `git diff` (staged + unstaged), `git log -5 --oneline`, `git branch -vv`, remote tracking.
2. **Map feature**:
   - Primary path under `services/<name>/` → service = `<name>`
   - Else `apps/<name>/` → service = `<name>`
   - Else platform-only → service = `platform`
   - Slug: 2–4 kebab words from the change intent (e.g. `facility-wa`, `docs-spec-picker`)
3. **Branch**: `feat/<service>-<slug>`
   - Base: `main` unless user names another base
   - `git fetch origin`
   - If branch missing locally and on `origin` → create from `origin/<base>` (or local base)
   - If exists remotely → checkout / track it; apply current work onto it
   - Do not force-push `main`/`master`
4. **Stage** only files for this feature (exclude secrets and unrelated junk).
5. **Commit** when user asked to ship/PR (that implies commit). Message: why-focused, 1–2 sentences. Use HEREDOC / `git commit -F -` (no unsupported `--trailer` on older git). Follow repo commit style from `git log`.
6. **Test + report + wait for approval** (see Hard gate).
7. **Push** (only after approval): `git push -u origin HEAD` (needs network as required). Prefer `HEAD:feat/<service>-<slug>` so a mis-tracked branch cannot update `main`.
8. **PR** with `gh pr create` (use `--repo strollbike/facinectCloud` if remote still points at the old org):
   - Title: `<service>: <short why>`
   - Body HEREDOC:

```markdown
## Summary
- …

## Test plan
- [ ] …
```

9. **Reply** with the PR URL. Stop.

## Multi-service one feature

One branch + one PR is OK. Title: primary service; mention `+ shared` if compose/docs/gateway also changed. Split into multiple PRs only if the user asks.

## Model note

Works with any Cursor agent model (including Grok). Skill logic is model-agnostic.

## Examples

- Notifications docs picker → `feat/web-docs-spec-picker` → PR title `web: add Service picker on /docs`
- Facility WhatsApp hybrid → `feat/notifications-facility-wa`
- New `services/booking` MVP → `feat/booking-mvp-api`
- Traefik file-provider only → `feat/platform-traefik-file-routes`
