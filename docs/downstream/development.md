# Long-lived Downstream Development Standard

## Goals and non-goals

The fork must support sustained product development while keeping upstream
updates reviewable, reversible, and cheap to integrate.

The goal is not a zero-conflict fork. The goal is small, attributable conflict
surfaces with explicit behavior tests and removal conditions. Do not create a
parallel copy of the upstream architecture merely to avoid touching it.

## Branch topology

```text
upstream/main ─────────────── authoritative upstream history
       │ fast-forward only
origin/main ───────────────── exact mirror; no downstream commits
       │ merge into a dedicated maintenance branch
downstream/main ───────────── integration and release history
       ├─ feature/<scope>
       ├─ fix/<scope>
       └─ maintenance/upstream-<date>-<sha>
```

- `origin` is the only normal push destination.
- `upstream` is fetch-only. The bootstrap disables its push URL.
- Never force-push or rebase published `main` or `downstream/main` history.
- Feature branches start from a fresh `downstream/main` and return through a
  focused PR whose base is explicitly `downstream/main`; do not rely on the
  GitHub default base. Production tags and images resolve to a commit on
  `downstream/main`.
- A contribution intended for upstream starts from `upstream/main` and must not
  depend on downstream-only code or deployment state.

## Extension design

Classify each change:

- `upstream-owned`: code whose structure and behavior follow upstream.
- `fork-extension`: a downstream package, file, component, or deployment profile.
- `integration-point`: the smallest registration, injection, interface, route,
  or setting hook needed to connect an extension to upstream-owned code.

Prefer a fork extension plus a narrow integration point. Do not duplicate a
controller/service/model stack, fork an entire provider adapter, or copy a large
upstream file only to change a small behavior.

Every active modification to upstream-owned code must have an entry in
`fork-deltas.yaml` with purpose, paths, conflict risk, verification, and a
condition for deletion or upstreaming. Keep upstream-sync commits, downstream
features, generated files, and database migrations in separable commits.

For risky behavior, prefer an explicit setting or feature flag whose disabled
state preserves upstream behavior. Flags need an owner and removal decision;
they are not permanent architecture.

## Upstream synchronization

Synchronize in small, regular batches and lock the exact upstream SHA.

1. Ensure the worktree is clean and run
   `scripts/agent-runtime/upstream-audit.sh --fetch`; the script independently
   refuses a dirty tree and does not rely on operator memory.
2. Fast-forward local `main` to `upstream/main`; push it to `origin/main`. If a
   fast-forward is impossible, stop: downstream commits leaked into the mirror.
3. Create `maintenance/upstream-<date>-<short-sha>` from fresh
   `downstream/main`.
4. Merge the locked `upstream/main` SHA into that branch. Do not combine feature
   work or opportunistic refactors.
5. Resolve conflicts under the rules below; update all affected fork-delta
   entries and record upstream behavior changes even when Git reported no text
   conflict.
6. Run focused tests, then the applicable full verification profiles. Database
   and deployment changes require their own staged evidence.
7. Re-fetch before push and prove the locked upstream SHA is an ancestor of the
   sync branch. Open a maintenance PR to `downstream/main` and read back PR HEAD,
   mergeability, checks, and the merged SHA separately.

Never use a force sync to make divergence disappear. A failed fast-forward,
unexplained migration, missing generated output, semantic conflict, or stale
fork-delta entry keeps the synchronization incomplete.

## Conflict handling

Git conflicts are only the visible subset. Review changed routes, controllers,
services, models, provider adapters, billing paths, settings, cache protocols,
background work, frontend i18n, migrations, Docker, and release configuration.

For each conflict:

1. Read the upstream change and its tests before selecting a resolution.
2. Preserve the current upstream structure and semantics by default.
3. Replay the smallest downstream integration point required by its ledger entry.
4. Never accept a whole file with `ours` or `theirs` for a semantic conflict.
5. Treat billing, auth, migrations, transactions, relay DTOs, provider protocol,
   and public API behavior as semantic conflicts requiring independent review.
6. Regenerate generated files using the owning tool; do not hand-merge output.
7. Add a regression test that proves the intended combined behavior when an
   existing test does not already do so.

`git rerere` is enabled without auto-staging. Reused resolutions are suggestions:
review the diff and tests before staging them.

## Database evolution

Database changes must work on SQLite, MySQL, and PostgreSQL and use an
expand-contract rollout:

- R0: code compatible with both old and expanded schema.
- R1: additive schema change, verified on all supported engines and on a
  restorable near-production backup in staging.
- R2: enable new behavior after schema readback and canary.
- Contract: remove compatibility only in a later release after rollback no longer
  depends on it.

Do not rewrite an already released migration. Backups count only when restore has
been exercised. Schema application, code deployment, and feature activation are
separate authorized actions and separate evidence.

## Release and production truth

A closeout reports these independently:

1. local exact HEAD and verification receipt;
2. fork PR, reviews, required checks, and merged SHA;
3. built image digest and deployed revision;
4. database/migration readback;
5. local service health and public API smoke.

Production rollout uses preflight, backup/restore proof, canary, explicit failure
thresholds, and rollback. Tags, GitHub releases, image publication, migrations,
secret changes, and production deployment require explicit authorization; the
presence of an upstream workflow is not authorization to run it.

## Maintenance cadence

- Weekly or before new feature work: fetch and run the upstream audit.
- Monthly: review active fork deltas, upstream candidates, stale flags, dependency
  security exceptions, and unverified database/provider surfaces.
- Before every release: sync audit, exact-HEAD full verification, deployment
  rendering, backup/rollback check, and public smoke plan.
- After every release: record deployed SHA/digest, migration state, smoke results,
  incidents, rollback decision, and any new conflict hotspot.

For the current single-node SGP1 deployment, a zero-impact production canary is
not available. Use the staging rehearsal and maintenance-window procedure in
`docs/downstream/sgp1-operations.md`; do not label a full-node replacement as a
canary.

## Primary references

- [GitHub: Configuring a remote repository for a fork](https://docs.github.com/en/pull-requests/how-tos/work-with-forks/configuring-a-remote-repository-for-a-fork)
- [GitHub: Syncing a fork](https://docs.github.com/en/pull-requests/how-tos/work-with-forks/syncing-a-fork)
- [Git: `git-rerere`](https://git-scm.com/docs/git-rerere)
- [Git: `git-merge-tree`](https://git-scm.com/docs/git-merge-tree)
