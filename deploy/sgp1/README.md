# SGP1 single-node deployment

This deployment runs New API, PostgreSQL, Redis, and Cloudflare Tunnel on one
Droplet. The application listens on `127.0.0.1:3000`; PostgreSQL and Redis are
not published to the host network. Cloudflare Tunnel reaches New API through a
shared network namespace, so no public HTTP port is required on the Droplet.

## Start

Create `deploy/sgp1/.env` from `.env.example`, replace every secret with a
unique random value, set the exact HTTPS origins, add the remotely managed
Cloudflare Tunnel token, then run from the repository root:

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
ssh -L 3000:127.0.0.1:3000 codex-silent-forge-ef4f
```

Then open `http://127.0.0.1:3000`. Do not publish port 3000 directly on the
internet. The production deployment enables Secure cookies, restricts trusted
browser origins to `SESSION_COOKIE_TRUSTED_URL`, and trusts only the loopback
Cloudflare Tunnel proxy.

## Update

Fetch the deployment branch, set `NEW_API_IMAGE_TAG` to the reviewed commit,
then rebuild. Back up PostgreSQL before an update because the master node runs
database migrations.

```bash
docker compose --env-file deploy/sgp1/.env \
  -f deploy/sgp1/docker-compose.yml exec -T postgres \
  pg_dump -U newapi -d newapi -Fc > newapi-$(date +%Y%m%d-%H%M%S).dump
```
