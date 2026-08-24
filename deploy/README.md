# ikik-api Deployment Guide

This directory contains the supported deployment entry points for [ipanel/SliderAPIv2](https://github.com/ipanel/SliderAPIv2).

## Recommended architecture

The standard Docker deployment starts three services:

- `ikik-api`: `ghcr.io/ipanel/sliderapiv2`, published for `linux/amd64` and `linux/arm64`
- `mariadb`: MariaDB 10.11
- `redis`: Redis 8

The application runs with `AUTO_SETUP=true`. On first startup it connects to MariaDB and Redis, applies migrations, creates the initial administrator, and writes generated configuration/state under `/app/data`. **The application data volume or `./data` directory must be persisted.**

## Files

| File | Purpose |
|---|---|
| `docker-compose.local.yml` | Recommended deployment with bind-mounted local data directories |
| `docker-compose.yml` | Deployment with Docker named volumes |
| `docker-compose.standalone.yml` | Application only; use external MariaDB and Redis |
| `docker-compose.dev.yml` | Build the image from the current local source tree |
| `.env.example` | Environment variable template |
| `docker-deploy.sh` | Installs or updates templates, generates secrets only on first install, pulls images, and starts the stack |
| `DOCKER.md` | Image-oriented deployment reference |
| `install.sh` | Linux binary/systemd installer |

## One-click Docker deployment

Requirements: Docker Engine with Docker Compose v2, OpenSSL, and either curl or wget.

Run the installer in a new empty directory:

```bash
mkdir -p sliderapiv2 && cd sliderapiv2
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash
```

The script:

1. downloads `docker-compose.local.yml` as `docker-compose.yml`;
2. downloads the current `.env.example`;
3. on a first install, creates `.env` and generates separate MariaDB root, MariaDB application, Redis, JWT, and TOTP secrets;
4. on an existing deployment, updates the templates but preserves `.env` and every persisted credential;
5. creates `data/`, `mariadb_data/`, and `redis_data/`;
6. pulls the GHCR image and starts the complete stack.

It does not print generated secrets. Back up `.env` securely together with the data directories. If persistent data exists but `.env` is missing, the script refuses to generate replacement credentials. A legacy deployment without the required MariaDB settings must be migrated explicitly instead of being overwritten.

For a non-interactive template update after taking a backup, pass `--yes`: `curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash -s -- --yes`. Existing `.env` values are still preserved.

Check the deployment:

```bash
docker compose ps
docker compose logs -f ikik-api
curl -f http://127.0.0.1:8080/health
```

If `ADMIN_PASSWORD` was left empty, the initial password is written to the first-start application log. Store it immediately and change it after signing in.

## Manual Docker deployment

```bash
git clone https://github.com/ipanel/SliderAPIv2.git
cd SliderAPIv2/deploy
cp .env.example .env
```

Edit `.env` and set at least these values:

```dotenv
IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:latest
MARIADB_ROOT_PASSWORD=<strong-random-value>
MARIADB_PASSWORD=<strong-random-value>
REDIS_PASSWORD=<strong-random-value>
JWT_SECRET=<64-hex-characters>
TOTP_ENCRYPTION_KEY=<64-hex-characters>
```

Then start the local-directory stack:

```bash
mkdir -p data mariadb_data redis_data
docker compose -f docker-compose.local.yml pull
docker compose -f docker-compose.local.yml up -d
docker compose -f docker-compose.local.yml ps
```

For Docker named volumes, use `docker-compose.yml` instead.

## Image versions and upgrades

The default image is:

```text
ghcr.io/ipanel/sliderapiv2:latest
```

For reproducible production deployments, pin a release in `.env`:

```dotenv
IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:vX.Y.Z
```

Upgrade a deployment with:

```bash
docker compose pull
docker compose up -d
docker image prune -f
```

Roll back by restoring the database/data backup, setting `IKIK_API_IMAGE` to the previous `vX.Y.Z`, and running `docker compose up -d` again. Database migrations may not be backward compatible, so do not roll back only the container image after a schema-changing release.

If GHCR returns `denied` while pulling, authenticate with an account/token that has `read:packages` permission:

```bash
echo "$GHCR_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USER --password-stdin
```

If GitHub Actions fails while publishing with `permission_denied: write_package`, grant `ipanel/SliderAPIv2` **Write** access in the existing `sliderapiv2` package's **Manage Actions access** settings. Alternatively, configure repository secrets `GHCR_TOKEN` (classic PAT with `write:packages` and `read:packages`) and `GHCR_USERNAME`; the workflow requires both secrets to be configured together; when both are absent it uses the repository `GITHUB_TOKEN`. Never store these credentials in `.env` or source files.

## Data and backup

Local-directory deployment paths:

```text
data/           application config, installation marker, logs, and local state
mariadb_data/   MariaDB data
redis_data/     Redis persistence
.env            secrets and deployment configuration
```

Stop writers before a consistent file-level backup:

```bash
docker compose down
tar -czf sliderapiv2-backup.tar.gz .env data mariadb_data redis_data docker-compose.yml
docker compose up -d
```

For larger production systems, use native MariaDB backup tools and test restoration regularly.

## External MariaDB and Redis

Use `docker-compose.standalone.yml` when the database and cache are managed outside the stack. Configure:

```dotenv
DATABASE_HOST=db.example.com
DATABASE_PORT=3306
DATABASE_USER=ikik_api
DATABASE_PASSWORD=<database-password>
DATABASE_DBNAME=ikik_api
DATABASE_SSLMODE=disable
REDIS_HOST=redis.example.com
REDIS_PORT=6379
REDIS_PASSWORD=<redis-password>
```

Start it with:

```bash
docker compose -f docker-compose.standalone.yml pull
docker compose -f docker-compose.standalone.yml up -d
```

`host.docker.internal` is available through `host-gateway` if the external service runs on the Docker host. Prefer an actual host name or private network address in production.

## OAuth credentials

No Google OAuth Client ID or Client Secret is embedded in the source code or image.

Set only the credentials required for the login flow you use:

```dotenv
# Custom Gemini AI Studio OAuth client
GEMINI_OAUTH_CLIENT_ID=
GEMINI_OAUTH_CLIENT_SECRET=

# Gemini CLI / Code Assist OAuth client
GEMINI_CLI_OAUTH_CLIENT_ID=
GEMINI_CLI_OAUTH_CLIENT_SECRET=

# Antigravity OAuth client
ANTIGRAVITY_OAUTH_CLIENT_ID=
ANTIGRAVITY_OAUTH_CLIENT_SECRET=
```

Leaving a pair empty disables that credential-dependent flow; it no longer falls back to a credential embedded in the repository.

## Reverse proxy

Terminate TLS at Nginx, Caddy, or a load balancer. Do not cache `/api/*`, `/v1/*`, streaming responses, or gateway routes. When using Nginx, preserve streaming and headers required by clients; projects using underscore headers commonly need this in the `http` block:

```nginx
underscores_in_headers on;
```

## Operations

```bash
# Status
docker compose ps

# Application logs
docker compose logs --tail=200 -f ikik-api

# MariaDB health
docker compose exec mariadb healthcheck.sh --connect --innodb_initialized

# Redis health (REDISCLI_AUTH is already injected into the container)
docker compose exec redis redis-cli ping

# Restart only the application
docker compose restart ikik-api

# Stop without deleting data
docker compose down
```

Never use `docker compose down -v` unless you intentionally want to delete named volumes.

## Troubleshooting

### `/api/v1/settings/public` or admin dashboard routes return 404

Both routes are part of the normal application router. A 404 immediately after deployment usually means the process is still serving the setup-only router because automatic initialization did not complete.

Check:

```bash
docker compose ps
docker compose logs --tail=300 ikik-api
docker compose logs --tail=200 mariadb
docker compose logs --tail=200 redis
```

Confirm all of the following:

- `AUTO_SETUP=true` is present in the application container;
- MariaDB and Redis are healthy and credentials match `.env`;
- `/app/data` is writable and persistent;
- the startup log reports successful migrations and server initialization.

After correcting the dependency or credential issue:

```bash
docker compose restart ikik-api
curl -f http://127.0.0.1:8080/health
```

Do not delete `/app/data` to work around setup failures; it contains the generated configuration and installation state.

### Image pull or architecture errors

```bash
docker buildx imagetools inspect ghcr.io/ipanel/sliderapiv2:latest
uname -m
```

The published manifest supports Linux AMD64 and Linux ARM64. Docker selects the matching platform automatically.

### Port conflict

Change the host port only:

```dotenv
SERVER_PORT=18080
```

The container continues listening on port `8080`.

## Binary installation

The Linux release archives contain `ikik-api` for AMD64 and ARM64. The installer and systemd unit are retained for non-Docker deployments:

```bash
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/install.sh | sudo bash
```

Binary deployments must provide their own MariaDB/Redis services and follow the same OAuth credential rules. See `config.example.yaml` and `ikik-api.service` before using this mode in production.
