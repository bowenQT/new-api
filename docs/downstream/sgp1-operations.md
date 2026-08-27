# SGP1 Release and Rollback Evidence

This is the downstream evidence contract for the existing single-node
DigitalOcean SGP1 deployment. It is not authorization to access the Droplet,
database, Cloudflare, publish an image, or deploy.

## Branch transition

`downstream/main` was initialized from the existing SGP1 deployment baseline
`332c63af2bcf10ae0bef97b0e6c5c9f54b312312`. Before the first release from the
new branch, read back the live checkout:

```bash
git fetch origin
git status --short --branch
git rev-parse HEAD
git rev-parse origin/downstream/main
```

Only when the live HEAD equals the recorded baseline and the worktree is clean
may an authorized operator switch its tracking branch to `downstream/main`.
Any difference stops the transition and requires a separate reconciliation;
never reset the Droplet to make it appear clean.

## Required release receipt

Record without secrets:

- source commit on `origin/downstream/main` and its exact remote readback;
- previous source commit and known-good image tag/digest;
- candidate image tag and immutable local/registry digest;
- PostgreSQL backup path outside the Git worktree, owner/mode, checksum, and a
  completed restore rehearsal result;
- migration plan, compatibility window, and rollback boundary;
- staging rehearsal result on production-like data;
- maintenance window, failure thresholds, and operator;
- post-deploy Compose state, local health, public health, auth rejection, tunnel,
  firewall/loopback exposure, and database readback.

The current topology has one application node. Therefore production cannot
provide a zero-impact traffic canary. Use a staging rehearsal, explicit
maintenance window, and immediate rollback threshold. Do not call a full-node
replacement a canary.

## Pre-deploy gate

1. Lock the candidate commit; run exact-HEAD `full` and `deployment` profiles.
2. Confirm the candidate is on `origin/downstream/main` and resolve the image tag
   to that exact commit. Do not deploy a floating tag.
3. Create a PostgreSQL logical backup and checksum it. Restore it into an isolated
   disposable PostgreSQL instance, run the minimum integrity/read queries, then
   destroy only that explicitly identified disposable instance.
4. Rehearse the Compose update and any migration in staging. For schema work,
   follow R0 compatible code -> R1 additive schema -> R2 activation.
5. Record rollback triggers before changing production: health failure, migration
   failure, elevated 5xx, auth/billing regression, queue/backlog growth, or public
   tunnel failure.

## Deployment readback

After an explicitly authorized deployment, record each layer independently:

```bash
git rev-parse HEAD
docker compose --env-file deploy/sgp1/.env -f deploy/sgp1/docker-compose.yml ps
curl --fail --silent --show-error http://127.0.0.1:3000/api/status
curl --fail --silent --show-error https://code-token.ai/api/status
curl --silent --show-error --output /dev/null --write-out '%{http_code}\n' \
  https://code-token.ai/v1/models
```

The unauthenticated models request must return the application's authentication
error status (currently expected `401`); a `200`, Cloudflare error, or origin
timeout fails the gate. Also read back the running image ID/digest, PostgreSQL
migration state, Cloudflare Tunnel health, UFW policy, and that the application
port remains bound only to loopback.

## Rollback

- For code/config-only failure, restore the recorded previous commit/image and
  recreate only the application service, then repeat every health/auth readback.
- If an additive migration was applied, keep it during code rollback; R0/R1 code
  must tolerate both schemas.
- If rollback would require destructive schema reversal or data loss, stop. Use
  the rehearsed restore procedure under separate database authorization.
- Preserve failed logs and the release receipt before retrying. A rollback is
  complete only after the previous image/digest and all public/local readbacks are
  confirmed.
