---
name: upstream-sync
description: Audit and synchronize QuantumNous/new-api upstream changes into the long-lived downstream fork while preserving fork deltas, database compatibility, and evidence-bound verification.
---

# Upstream Sync

Use this skill when asked to check, compare, merge, update, or synchronize the
downstream fork with upstream.

## Required sources

Resolve `repo_root=$(git rev-parse --show-toplevel)`, then read completely before
acting:

- `$repo_root/AGENTS.md`
- `$repo_root/.agents/runtime/README.md`
- `$repo_root/docs/downstream/development.md`
- `$repo_root/docs/downstream/fork-deltas.yaml`
- the tests and runbooks owning every affected high-risk path

## Procedure

1. Run `scripts/agent-runtime/preflight.sh --mode sync --fetch` and
   `scripts/agent-runtime/upstream-audit.sh --no-fetch`.
2. Lock and report exact `upstream/main`, `origin/main`, `downstream/main`, and
   merge-base SHAs. Do not infer freshness from branch names.
3. Stop if `origin/main` is not an exact mirror, the worktree is dirty, a Git
   operation is active, remote identity is unexpected, or upstream push is not
   disabled.
4. Fast-forward the mirror separately. Never rewrite its history.
5. Create a dedicated maintenance branch from fresh `downstream/main` and merge
   the locked upstream SHA. Do not include product work.
6. Review both text conflicts and behavior changes. At minimum inspect routes,
   services, models/migrations, relay/provider protocols, billing/auth, settings,
   caches/background work, frontend/i18n, Docker, and workflows.
7. Preserve upstream structure; replay only documented integration points. Update
   the fork-delta ledger. Never resolve semantic conflicts with whole-file
   `ours`/`theirs`.
8. Run the verification matrix. Database or production-adjacent changes remain
   incomplete without their dedicated evidence.
9. Re-fetch before push; prove the locked upstream SHA is an ancestor of the
   candidate HEAD. Push to `origin`, then read back PR HEAD, mergeability, checks,
   and merged SHA independently.

Force push, migration apply, release/tag/image publication, deployment, secret
changes, and destructive cleanup require explicit authorization.
