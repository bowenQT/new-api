# Downstream Agent Runtime

This is the repo-local operating contract for long-lived downstream development.
It adds a thin, executable layer around the upstream project rules in `AGENTS.md`;
it does not replace them or create a parallel source of truth.

## Authority order

For every task, use this order:

1. The user's current, explicit instruction.
2. The nearest applicable `AGENTS.md`, actual code, tests, and live Git/runtime state.
3. `docs/downstream/development.md` and the active entries in
   `docs/downstream/fork-deltas.yaml`.
4. Plans, receipts, and historical notes as recall only.

When documentation and current code or live state disagree, stop relying on the
stale statement and report the discrepancy.

## Runtime entry flow

1. Run `scripts/agent-runtime/preflight.sh --mode edit` before changing files.
   Use `--mode read-only` for inspection and `--mode sync --fetch` for upstream
   work. Edit mode refuses the stable `main` branches even when clean.
2. Classify the task as S1, S2, or S3 below.
3. Read the owning code, tests, nested rules, and relevant runbook before planning.
4. For S2/S3 work, fill `.agents/runtime/task-envelope.md` in the task/PR notes.
   S1 work does not require a retained plan.
5. Use a dedicated feature worktree for parallel work and normally for S2/S3.
   Do not switch the shared stable worktree away from its integration branch.
6. Run the verification profiles required by
   `.agents/runtime/verification-matrix.md`.
7. Close repository delivery with `scripts/agent-runtime/closeout.sh` and record
   the remaining delivery surfaces in `.agents/runtime/closeout.md`. A green
   command is evidence only for the exact revision, environment, and scope it
   exercised.

## Risk classes

### S1 — contained

Docs, tests, local tooling, or a small implementation change with no API,
billing, auth, database, provider-protocol, deployment, or production effect.

- A short goal and focused verification are sufficient.
- Do not create checkpoints, orchestration state, or multi-review ceremony.

### S2 — contract or multi-surface

Changes spanning modules or affecting API behavior, relay/provider adapters,
frontend routes, shared DTOs, caching, CI, fork synchronization, or staging.

- Record goal, non-goals, mutation scope, affected contracts, and rollback.
- Add or update behavior-level regression tests.
- Use disjoint file ownership for parallel agents and independently review their
  output before integration.

### S3 — high consequence

Billing/quota, authentication/authorization, database schema or transactions,
secrets, production configuration, release, deploy, destructive Git operations,
or externally visible bulk actions.

- Require an explicit plan, failure thresholds, rollback, and independent review.
- Production writes, releases, tags, force pushes, migrations, secret access, and
  destructive cleanup require explicit user authorization for that action.
- Use preflight -> staging rehearsal -> canary where topology supports it ->
  readback -> staged expansion. A merge, deploy, migration, and runtime
  activation are four different facts.

If a task discovers a higher-risk surface, reclassify it before continuing.

## Agent and worktree rules

- Dispatch packets must state the repo, base/head, goal, read/write scope, owned
  files or evidence, forbidden actions, validation contract, and output format.
- Two writing agents must not own overlapping files. The parent agent integrates
  or rejects results with evidence; a subagent's completion claim is not proof.
- Never copy `.env` or production credentials into a worktree by default.
- Never use broad staging such as `git add -A`; stage only reviewed task paths.
- Worktree audit is read-only. Deletion requires clean state, no active process,
  no open PR, and direct merge evidence; uncertainty means keep it.

## Evidence rules

`scripts/agent-runtime/verify.sh` records successful receipts under the Git
common directory (`.git/agent-runtime/receipts`), so evidence is local and cannot
be committed accidentally. `scripts/agent-runtime/closeout.sh` consumes a receipt
and revalidates its exact HEAD, profile, workspace, dependency, and toolchain
fingerprints. With `--pr`, it also requires the exact PR head, `downstream/main`
base, non-draft mergeability, and current passing checks.

- Default verification requires a clean worktree and therefore binds to exact
  `HEAD`.
- `--allow-dirty` is for iteration only. Its receipt is labeled
  `WORKTREE_SNAPSHOT` and must not be reported as exact-HEAD verification.
- A receipt is invalid if HEAD or the workspace fingerprint changes during the
  run, or if its environment and command scope differ from the claim.
- CI, PR mergeability, image digest, deployment, database state, and public API
  smoke are separate readbacks. None substitutes for another.
- The executable closeout claims repository delivery only. It never merges or
  claims database/production state; those remain explicit `NOT VERIFIED` fields
  until separately read back.

## GitHub fork CI bootstrap

GitHub does not run Actions workflows in a newly created fork until a maintainer
explicitly enables them. Before making CI contexts required on
`downstream/main`, enable Actions for the fork, confirm `.github/workflows/ci.yml`
is registered from the default branch, and trigger a reviewed PR event. A PR
opened before activation needs a new `synchronize` event; do not weaken or
temporarily bypass required checks to bootstrap it. See GitHub's
[fork workflow event documentation](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#workflows-in-forked-repositories).

## Fork maintenance

The branch and conflict contract is in `docs/downstream/development.md`. Before an
upstream update, read `.agents/skills/upstream-sync/SKILL.md` and run:

```bash
scripts/agent-runtime/upstream-audit.sh --fetch
```

Do not resolve a semantic conflict with wholesale `ours` or `theirs`. Preserve
the current upstream structure, replay the smallest required downstream hook,
update the fork-delta ledger, and validate the resulting behavior.
