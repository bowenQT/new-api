# Verification Matrix

Run focused tests while iterating, then the applicable closeout profile. Commands
below are the minimum, not a substitute for change-specific regression coverage.

| Change surface | Minimum closeout evidence |
| --- | --- |
| Docs, runtime, governance | `scripts/agent-runtime/verify.sh docs` |
| Root Go module | focused package tests, then `scripts/agent-runtime/verify.sh backend` |
| `relaykit/` | `scripts/agent-runtime/verify.sh relaykit`; independent `GOWORK=off` build is mandatory |
| Frontend | focused Vitest, then `scripts/agent-runtime/verify.sh frontend`; add a real browser check for changed interaction |
| Cross-module/API/relay/auth/billing | `scripts/agent-runtime/verify.sh full` plus behavior-specific tests and contract trace |
| SQLite/MySQL/PostgreSQL schema or DB behavior | `full` plus `scripts/agent-runtime/verify.sh database`; use dedicated disposable MySQL and PostgreSQL test databases with no pre-existing `tokens` table |
| `deploy/sgp1`, Dockerfile, Compose | `scripts/agent-runtime/verify.sh deployment`; add image build when the build chain changed |
| Production deployment | follow `docs/downstream/sgp1-operations.md`; record exact commit/image digest, backup/restore evidence, Compose and public health, unauthenticated API rejection, and rollback threshold/readback |

## Profile contents

- `docs`: runtime file presence, executable bits, shell syntax, protected policy
  pointer, and Git whitespace checks.
- `backend`: root vet/build and `make test` with a temporary ignored frontend
  embed placeholder when needed.
- `relaykit`: independent vet/build/test with `GOWORK=off`.
- `frontend`: frozen Bun install, lint, typecheck, Vitest, format/copyright checks,
  and production build.
- `full`: frontend production build followed by backend embed build, root tests,
  and independent relaykit vet/build. It does not claim live database or
  provider coverage.
- `database`: runs the existing model/controller database contracts with
  `TEST_MYSQL_DSN` and `TEST_POSTGRES_DSN`. Missing DSNs fail. The external
  MySQL/PostgreSQL migration tests must each report `PASS`; a skipped test does
  not produce a receipt. The runtime does not echo the DSN variables.
- `deployment`: read-only Compose rendering with `.env.example`; its receipt is
  bound to the Docker Compose version. It does not start containers or prove
  production health.

`bun run i18n:sync` is a write operation. Run it deliberately, review its diff,
then run the frontend profile; do not treat it as a read-only check.

For exact repository closeout, run:

```bash
scripts/agent-runtime/closeout.sh \
  --expected-head "$(git rev-parse HEAD)" \
  --profile <profile> \
  --pr <number>
```
