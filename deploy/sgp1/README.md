# SGP1 single-node deployment

This deployment runs New API, PostgreSQL, and Redis on one Droplet. The
application listens on `127.0.0.1:3000`; PostgreSQL and Redis are not published
to the host network.

## Start

Create `deploy/sgp1/.env` from `.env.example`, replace every secret with a
unique random value, then run from the repository root:

```bash
docker compose --env-file deploy/sgp1/.env \
  -f deploy/sgp1/docker-compose.yml up -d --build
```

Check the local health endpoint:

```bash
curl --fail --silent http://127.0.0.1:3000/api/status
```

Until a production domain and TLS certificate are configured, access the UI
through an SSH tunnel:

```bash
ssh -L 3000:127.0.0.1:3000 codex-silent-forge-ef4f
```

Then open `http://127.0.0.1:3000`. Do not publish port 3000 directly on the
internet. When HTTPS is added, set `SESSION_COOKIE_SECURE=true`, configure the
exact `SESSION_COOKIE_TRUSTED_URL`, and replace `TRUSTED_PROXIES=none` with the
reverse proxy's exact address or CIDR.

## Update

Fetch the deployment branch, set `NEW_API_IMAGE_TAG` to the reviewed commit,
then rebuild. Back up PostgreSQL before an update because the master node runs
database migrations.

```bash
docker compose --env-file deploy/sgp1/.env \
  -f deploy/sgp1/docker-compose.yml exec -T postgres \
  pg_dump -U newapi -d newapi -Fc > newapi-$(date +%Y%m%d-%H%M%S).dump
```
