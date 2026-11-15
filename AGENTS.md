# AGENTS.md

## Quick Reference for AI Agents Working on cf-tunnel-sync

This file provides condensed guidance for AI agents implementing this project.
For full details, see `docs/PLAN.md`.

## Core Rules

1. **Test-first (RED/GREEN)**: Write tests before implementation. Tests must compile and fail on assertions, not panic.
2. **One step per PR**: Each implementation step (S1-S12) is a separate branch `step/Sn-<slug>`.
3. **No scope creep**: Only implement what's in the current step. Extra ideas go in `docs/FUTURE.md`.
4. **No secrets**: Never commit tokens, keys, or credentials. Use the `Secret` type.
5. **Clean-room**: Do not copy DockFlare source. Implement from behavior specs only.
6. **Gates**: AUTO gates must pass (`make ci`). HUMAN gates require the owner — stop and wait.

## Project Structure

See `docs/PLAN.md` §5 for the architecture layout.

## Key Packages

- `internal/cli` — cobra commands
- `internal/config` — config loading, Secret type, schema
- `internal/logx` — logging with ring buffer
- `internal/labels` — label parser (pure)
- `internal/rules` — rule lifecycle and ingress rendering (pure)
- `internal/state` — JSON state persistence
- `internal/docker` — Docker API wrapper
- `internal/cf` — Cloudflare API wrapper
- `internal/reconcile` — reconciliation engine
- `internal/web` — HTTP status page
- `internal/stats` — metrics and counters

## Dependencies

See `docs/PLAN.md` §2 for the resolved stack decisions.

## Commit Conventions

Use Conventional Commits:
- `test(<scope>): <behaviour> (RED)`
- `feat(<scope>): ...`
- `refactor(<scope>): ...`
- `chore: ...`
- `build: ...`
- `docs: ...`

## CI

Run `make ci` before committing. It runs: format check, vet, lint, tests.

## Full Plan

See `docs/PLAN.md` for the complete implementation plan, behavior specs, and step-by-step guide.
