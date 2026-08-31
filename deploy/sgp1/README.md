# SGP1 single-node deployment

This deployment runs New API, Redis, a local rollback PostgreSQL instance, and
Cloudflare Tunnel on one Droplet. New API connects to DigitalOcean Managed
PostgreSQL over the SGP1 VPC with certificate verification. The application
listens on `127.0.0.1:3000`; the local PostgreSQL and Redis services are not
published to the host network. Cloudflare Tunnel reaches New API through a
dedicated Docker network, so no public HTTP port is required on the Droplet.

## Start

Create `deploy/sgp1/.env` from `.env.example`, set `NEW_API_IMAGE_TAG` to the
full 40-character SHA of the reviewed commit, replace every secret with a unique
random value, set `SQL_DSN` to the private managed PostgreSQL connection string,
set `MANAGED_POSTGRES_CA_CERT_PATH` to the absolute host path of its CA
certificate, set the exact HTTPS origins, add the remotely managed Cloudflare
Tunnel token, then run from the repository root:

```bash
docker compose --env-file deploy/sgp1/.env \
  -f deploy/sgp1/docker-compose.yml up -d --build
```

Check the local health endpoint:

```bash
curl --fail --silent http://127.0.0.1:3000/api/status
```

For local diagnostics, access the UI through an SSH tunnel:

```bash
ssh -L 3000:127.0.0.1:3000 operator@droplet-host
```

Then open `http://127.0.0.1:3000`. Do not publish port 3000 directly on the
internet. The production deployment enables Secure cookies, restricts trusted
browser origins to `SESSION_COOKIE_TRUSTED_URL`, and trusts only the Cloudflare
Tunnel container's fixed private address.

## Update

Follow the [SGP1 release and rollback evidence
contract](../../docs/downstream/sgp1-operations.md). Fetch
`origin/downstream/main`, verify the reviewed commit is on that branch, and use
its full SHA as `NEW_API_IMAGE_TAG`; do not continue tracking the historical
`deploy/sgp1` branch.

Back up managed PostgreSQL before an update because the master node runs
database migrations. Store the dump outside the Git worktree in a
permission-restricted directory, record its checksum, and complete the restore
rehearsal required by the operations contract. The local PostgreSQL service and
volume remain available only for the explicitly bounded rollback window.

```bash
(
  set -euo pipefail
  git fetch origin downstream/main
  reviewed_commit='REPLACE_WITH_FULL_REVIEWED_SHA'
  [[ "$reviewed_commit" =~ ^[0-9a-f]{40}$ ]]
  git merge-base --is-ancestor "$reviewed_commit" origin/downstream/main
  git switch downstream/main
  git pull --ff-only origin downstream/main
  test "$(git rev-parse HEAD)" = "$reviewed_commit"
  test -z "$(git status --porcelain=v1 --untracked-files=all)"

  backup_dir=/var/backups/new-api/postgres
  backup_owner=$(id -un)
  sudo install -d -m 0700 -o "$backup_owner" "$backup_dir"
  backup_path="$backup_dir/newapi-$(date -u +%Y%m%dT%H%M%SZ).dump"
  partial_path="$backup_path.partial"
  checksum_path="$backup_path.sha256"
  checksum_partial="$checksum_path.partial"
  trap 'rm -f -- "$partial_path" "$checksum_partial"' EXIT

  umask 077
  sql_dsn=$(sed -n 's/^SQL_DSN=//p' deploy/sgp1/.env)
  ca_cert_path=$(sed -n 's/^MANAGED_POSTGRES_CA_CERT_PATH=//p' deploy/sgp1/.env)
  test -n "$sql_dsn"
  test -f "$ca_cert_path"
  docker run --rm --network host \
    -e SQL_DSN="$sql_dsn" \
    -v "$ca_cert_path:/etc/new-api/db/ca-certificate.crt:ro" \
    postgres:16-alpine sh -ec 'pg_dump "$SQL_DSN" -Fc' > "$partial_path"
  docker run --rm -i postgres:16-alpine \
    pg_restore --list < "$partial_path" > /dev/null
  mv -- "$partial_path" "$backup_path"
  sha256sum "$backup_path" > "$checksum_partial"
  chmod 0600 "$backup_path" "$checksum_partial"
  mv -- "$checksum_partial" "$checksum_path"
  printf 'backup=%s\nchecksum=%s\n' "$backup_path" "$checksum_path"
)
```

The `pg_restore --list` check above verifies that the archive can be read; it is
not a restore rehearsal. Before deployment, restore the dump into an explicitly
identified, isolated disposable PostgreSQL instance and record the integrity and
minimum read checks required by the
[SGP1 operations contract](../../docs/downstream/sgp1-operations.md).
