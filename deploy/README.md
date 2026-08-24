# ikik-api Deployment Guide

This directory contains the supported deployment entry points for [ipanel/SliderAPIv2](https://github.com/ipanel/SliderAPIv2).

## Database choice

Docker deployment is now **SQLite-first**:

- SQLite is the default for new installations. It requires only `ikik-api` and Redis and stores `ikik-api.db` under the persistent `/app/data` directory.
- MariaDB remains available through the explicit `*.mysql.yml` Compose files.
- New installations open the web setup wizard by default; SQLite is preselected and MySQL remains available.
- Changing `DATABASE_DRIVER` does not migrate data. Back up the current database and perform an explicit migration before changing engines.

SQLite is recommended for a single application instance and simple self-hosted installations. Use MariaDB when running multiple application replicas or when you need an external database service and its operational tooling.

## Compose files

| File | Services and purpose |
|---|---|
| `docker-compose.local.yml` | **Recommended SQLite deployment** with bind-mounted `data/` and `redis_data/` |
| `docker-compose.yml` | SQLite deployment with Docker named volumes |
| `docker-compose.dev.yml` | Build local source and run with SQLite |
| `docker-compose.local.mysql.yml` | MariaDB deployment with bind-mounted `data/`, `mariadb_data/`, and `redis_data/` |
| `docker-compose.mysql.yml` | MariaDB deployment with Docker named volumes |
| `docker-compose.dev.mysql.yml` | Build local source and run with MariaDB |
| `docker-compose.standalone.yml` | Application only; defaults to SQLite and requires external Redis, with external MySQL/MariaDB optional |
| `.env.example` | Environment template for bundled SQLite/MariaDB Compose stacks |
| `.env.standalone.example` | Standalone template; defaults external services to `host.docker.internal` |
| `docker-deploy.sh` | Safe installer/updater that preserves the selected database engine and existing `.env` |
| `DOCKER.md` | Container-image and standalone examples |
| `install.sh` | Linux binary/systemd installer |

Do not combine the SQLite and MySQL Compose files as base/override files. Each one is a complete stack.

## One-click deployment

Docker Engine with Docker Compose v2 is always required. New installations additionally require OpenSSL and either curl or wget because the installer downloads templates and generates secrets locally.

### SQLite (default)

```bash
mkdir -p sliderapiv2 && cd sliderapiv2
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash
```

Equivalent explicit form:

```bash
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash -s -- --database sqlite
```

This starts `ikik-api` and Redis. The SQLite database is stored at `data/ikik-api.db` on the host. The host port binds to `127.0.0.1` by default so an unauthenticated first-run setup page is not exposed to the network. Complete setup locally (or through an SSH tunnel/reverse proxy), then set `BIND_HOST=0.0.0.0` only when external access is intentionally protected.

### MariaDB (optional)

```bash
mkdir -p sliderapiv2 && cd sliderapiv2
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash -s -- --database mysql
```

This starts `ikik-api`, MariaDB, and Redis and creates `mariadb_data/`.

The short flags `--sqlite` and `--mysql` are also accepted. For a non-interactive update after taking a backup, add `--yes`.

The installer:

1. selects the complete SQLite or MariaDB Compose file; new installs use local directories and existing named-volume installs keep named volumes;
2. resolves the newest strict `vX.Y.Z` tag, then downloads Compose and `.env.example` from that exact tag;
3. creates `.env` only on a new installation and pins `IKIK_API_IMAGE` to the same release tag;
4. generates Redis, JWT, and TOTP secrets, plus MariaDB passwords only in MySQL mode;
5. preserves existing credentials, database type, Compose file, and custom volume mappings during upgrades, while advancing the official `IKIK_API_IMAGE` tag;
6. refuses an automatic database-engine switch;
7. refuses to generate replacement credentials if persistent data or an existing Compose file is present but `.env` is missing.

A legacy deployment whose `.env` has no `DATABASE_DRIVER` is treated as MySQL, because previous official Compose files were MariaDB-only. This prevents an upgrade from silently opening an empty SQLite database.

**Never replace an existing MariaDB deployment?s `docker-compose.yml` with the new default SQLite template.** Keep using its original MySQL/MariaDB Compose file (or the preserved one-click file) and update only the image tag. Replacing the file does not migrate data and can omit the MariaDB service.

New installations write a stable `COMPOSE_PROJECT_NAME=sliderapiv2` value. During upgrades, the installer reuses an explicit value or discovers the existing project from Docker container/volume labels; if a named/custom-volume deployment cannot be identified safely, it stops instead of attaching empty volumes. The automatic updater only manages deployments whose selected stack is stored as `./docker-compose.yml`. It verifies existing container provenance and refuses `COMPOSE_FILE`, standard override files, and any other YAML file that looks like a Compose stack, because silently choosing the wrong file could change ports, networks, environment values, or storage mounts. Update manual `-f` deployments with the exact original file list.

Older Compose files that inject connection settings as runtime `DATABASE_*` or `REDIS_*` variables are also rejected until the `ikik-api` service entries are renamed to `SETUP_DATABASE_*` and `SETUP_REDIS_*`. Do not rename `DATABASE_MAX_*`/`REDIS_POOL_*` tuning variables, and keep `REDIS_PASSWORD` on the Redis service itself. The application also fails fast if a setup-only key and its legacy runtime key are both present, even when their values match. This one-time migration prevents environment variables from overriding `/app/data/config.yaml` after every restart.

Check the deployment:

```bash
docker compose -f ./docker-compose.yml ps
docker compose -f ./docker-compose.yml logs -f ikik-api
curl -f http://127.0.0.1:8080/health
```

New installations open `http://127.0.0.1:8080` in setup mode. SQLite is preselected. Use Redis host `redis` and the generated `REDIS_PASSWORD` from `.env`. For the bundled MariaDB stack, select MySQL and use host `mariadb`, the `MARIADB_USER` value, and `MARIADB_PASSWORD` from `.env`.

If you explicitly enable `AUTO_SETUP=true` and leave `ADMIN_PASSWORD` empty, the generated initial password is written to the first-start application log. Store it immediately and change it after signing in.

## Manual deployment

```bash
git clone https://github.com/ipanel/SliderAPIv2.git
cd SliderAPIv2/deploy
cp .env.example .env
chmod 600 .env
```

Set stable production values in `.env`:

```dotenv
IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:vX.Y.Z
REDIS_PASSWORD=<strong-random-value>
JWT_SECRET=<64-hex-characters>
TOTP_ENCRYPTION_KEY=<64-hex-characters>
```

### Manual SQLite deployment

Keep the browser-based default:

```dotenv
AUTO_SETUP=false
DATABASE_DRIVER=sqlite
DATABASE_PATH=ikik-api.db
```

Then start:

```bash
mkdir -p data redis_data
docker compose -f docker-compose.local.yml pull
docker compose -f docker-compose.local.yml up -d
```

For Docker named volumes, use `docker-compose.yml`.

### Manual MariaDB deployment

Also set:

```dotenv
AUTO_SETUP=false
DATABASE_DRIVER=mysql
MARIADB_ROOT_PASSWORD=<strong-random-value>
MARIADB_PASSWORD=<strong-random-value>
```

Then start the complete MySQL stack:

```bash
mkdir -p data mariadb_data redis_data
docker compose -f docker-compose.local.mysql.yml pull
docker compose -f docker-compose.local.mysql.yml up -d
```

For Docker named volumes, use `docker-compose.mysql.yml`.

## Web setup wizard

New Compose installations use `AUTO_SETUP=false` and open the browser setup wizard at `http://127.0.0.1:8080`. SQLite is selected by default, and MySQL can be selected on the same page.

Keep the choice consistent with the infrastructure you started:

- SQLite Compose files start `ikik-api + Redis`; keep SQLite selected, use Redis host `redis`, and copy `REDIS_PASSWORD` from `.env`.
- `*.mysql.yml` files additionally start MariaDB; select MySQL, use database host `mariadb`, and copy `MARIADB_USER`/`MARIADB_PASSWORD` from `.env`.
- `docker-compose.standalone.yml` runs the application alone, defaults to SQLite, requires an external Redis service, and can use an external MySQL/MariaDB server when `DATABASE_DRIVER=mysql`. Copy `.env.standalone.example` rather than the bundled-stack `.env.example`; for MySQL, set `DATABASE_HOST` to a host reachable from the container.

For unattended installation, set `AUTO_SETUP=true` before the first startup. In that mode `DATABASE_DRIVER` and the other setup values in `.env` are applied without showing the wizard.

The wizard writes `config.yaml` and `.installed` under `/app/data`. Persist that directory. After installation, changing only `.env` or selecting another Compose file is **not** a database migration.

## Custom upstream URL policy

Compose deployments use the same compatibility defaults as direct binary deployments: the URL allowlist is disabled, and self-hosted `http://` or private-network upstreams are accepted. The model sync and model probe endpoints use this same policy.

For an Internet-facing hardened deployment, opt in to strict validation and list every permitted upstream hostname:

```dotenv
SECURITY_URL_ALLOWLIST_ENABLED=true
SECURITY_URL_ALLOWLIST_ALLOW_INSECURE_HTTP=false
SECURITY_URL_ALLOWLIST_ALLOW_PRIVATE_HOSTS=false
SECURITY_URL_ALLOWLIST_UPSTREAM_HOSTS=api.openai.com,api.anthropic.com,your-upstream.example.com
```

Strict allowlist mode requires HTTPS. After changing these values, recreate the application container so the environment is reapplied.

## Upgrades and database safety

Pin a release tag in production:

```dotenv
IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:vX.Y.Z
```

Always pass the exact Compose file used by the deployment so a shell-level `COMPOSE_FILE` value cannot select another stack. A one-click deployment stores its selected template as `./docker-compose.yml`; a manual local deployment may instead use `./docker-compose.local.yml` or `./docker-compose.local.mysql.yml`.

```bash
COMPOSE_PATH=./docker-compose.yml
docker compose -f "$COMPOSE_PATH" pull
docker compose -f "$COMPOSE_PATH" up -d
```

The one-click updater detects the existing `.env` and keeps its database type. Passing a conflicting `--sqlite` or `--mysql` flag is rejected.

### SQLite backup

The following container-copy procedure covers both bind mounts and Docker named volumes. Stop the application and Redis first so the SQLite database, WAL, shared-memory files, and Redis snapshot are copied consistently:

```bash
set -euo pipefail
umask 077
COMPOSE_PATH=./docker-compose.yml
BACKUP_DIR="backups/sqlite-$(date +%F-%H%M%S)"
mkdir -p "$BACKUP_DIR/app-data" "$BACKUP_DIR/redis-data"
services_stopped=false
restore_services() {
  if [ "$services_stopped" = true ]; then
    docker compose -f "$COMPOSE_PATH" start redis ikik-api >/dev/null || true
  fi
}
trap restore_services EXIT

services_stopped=true
docker compose -f "$COMPOSE_PATH" stop ikik-api redis
APP_CONTAINER=$(docker compose -f "$COMPOSE_PATH" ps -aq ikik-api)
REDIS_CONTAINER=$(docker compose -f "$COMPOSE_PATH" ps -aq redis)
docker cp "${APP_CONTAINER}:/app/data/." "$BACKUP_DIR/app-data"
docker cp "${REDIS_CONTAINER}:/data/." "$BACKUP_DIR/redis-data"
cp .env "$BACKUP_DIR/.env"
cp "$COMPOSE_PATH" "$BACKUP_DIR/docker-compose.yml"
docker compose -f "$COMPOSE_PATH" start redis ikik-api
services_stopped=false
trap - EXIT
```

For a manual bind-mounted deployment, `data/ikik-api.db` is the default database file. Do not run multiple ikik-api replicas against the same SQLite file.

### MariaDB backup

Stop the application before creating a logical database dump so no application write can fall between the SQL snapshot and the copied `/app/data`/Redis state. The password is not expanded into the host command line, and the cleanup trap restarts services if a later backup step fails. This works with bind mounts and named volumes:

```bash
# One-click installs use ./docker-compose.yml. For a manual bind-mount
# MariaDB deployment, use ./docker-compose.local.mysql.yml instead.
set -euo pipefail
umask 077
COMPOSE_PATH=./docker-compose.yml
BACKUP_DIR="backups/mysql-$(date +%F-%H%M%S)"
mkdir -p "$BACKUP_DIR/app-data" "$BACKUP_DIR/redis-data"
services_stopped=false
restore_services() {
  if [ "$services_stopped" = true ]; then
    docker compose -f "$COMPOSE_PATH" start redis ikik-api >/dev/null || true
  fi
}
trap restore_services EXIT

# Stop writers before the SQL snapshot so database and application state share one cutoff.
services_stopped=true
docker compose -f "$COMPOSE_PATH" stop ikik-api
docker compose -f "$COMPOSE_PATH" exec -T mariadb sh -ec '
  defaults_file=$(mktemp)
  trap "rm -f $defaults_file" EXIT
  chmod 600 "$defaults_file"
  printf "[client]\nuser=%s\npassword=%s\n" "$MARIADB_USER" "$MARIADB_PASSWORD" > "$defaults_file"
  mariadb-dump --defaults-extra-file="$defaults_file" --single-transaction "$MARIADB_DATABASE"
' > "$BACKUP_DIR/database.sql"

docker compose -f "$COMPOSE_PATH" stop redis
APP_CONTAINER=$(docker compose -f "$COMPOSE_PATH" ps -aq ikik-api)
REDIS_CONTAINER=$(docker compose -f "$COMPOSE_PATH" ps -aq redis)
docker cp "${APP_CONTAINER}:/app/data/." "$BACKUP_DIR/app-data"
docker cp "${REDIS_CONTAINER}:/data/." "$BACKUP_DIR/redis-data"
cp .env "$BACKUP_DIR/.env"
cp "$COMPOSE_PATH" "$BACKUP_DIR/docker-compose.yml"
docker compose -f "$COMPOSE_PATH" start redis ikik-api
services_stopped=false
trap - EXIT
```

Do not archive a live `mariadb_data/` directory as if it were a consistent database backup. Use the logical dump above, `mariadb-backup`, or a storage snapshot taken while MariaDB is stopped. Do not use `docker compose -f "$COMPOSE_PATH" down -v` unless destroying all persisted data is intentional.

## GHCR access

The image is:

```text
ghcr.io/ipanel/sliderapiv2
```

If a private package returns `denied` while pulling, authenticate with a token that has `read:packages`:

```bash
echo "$GHCR_TOKEN" | docker login ghcr.io -u YOUR_GITHUB_USER --password-stdin
```

If GitHub Actions reports `permission_denied: write_package`, grant `ipanel/SliderAPIv2` Write access in the package's **Manage Actions access** settings, or configure both `GHCR_USERNAME` and a classic `GHCR_TOKEN` with `write:packages` and `read:packages`. If the old package was deleted, re-running the workflow normally recreates it under this repository; no deployment filename change is required.

## OAuth credentials

No Google OAuth client secret is embedded in this repository or image. Provide optional credentials through environment variables:

```text
GEMINI_CLI_OAUTH_CLIENT_ID
GEMINI_CLI_OAUTH_CLIENT_SECRET
ANTIGRAVITY_OAUTH_CLIENT_ID
ANTIGRAVITY_OAUTH_CLIENT_SECRET
```

Do not commit their values.

## 404 troubleshooting

If `/health` succeeds but routes such as `/api/v1/settings/public` or `/api/v1/admin/dashboard/users-ranking` return 404, inspect startup logs:

```bash
docker compose -f ./docker-compose.yml logs --tail=200 ikik-api
```

The process may still be running in setup-only mode because initialization failed, `/app/data` is not writable, or the selected database service is unavailable. Verify:

```bash
docker compose -f ./docker-compose.yml ps
ls -la data
```

For SQLite, confirm that `data/ikik-api.db`, `data/config.yaml`, and `data/.installed` can be created. For MariaDB, also confirm that the `mariadb` service is healthy and credentials match `.env`.

## Binary deployment

```bash
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/install.sh | sudo bash
```

Binary deployments must provide Redis and either a writable SQLite data directory or a reachable MySQL/MariaDB service. Review `config.example.yaml` and `ikik-api.service` before production use.
