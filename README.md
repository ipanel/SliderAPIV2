# ikik-api

![Go](https://img.shields.io/badge/Go-1.27.0-00ADD8?logo=go&logoColor=white)
![Vue](https://img.shields.io/badge/Vue-3-42b883?logo=vuedotjs&logoColor=white)
![MariaDB](https://img.shields.io/badge/MariaDB-10.11+-003545?logo=mariadb&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-7+-DC382D?logo=redis&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)
![License](https://img.shields.io/badge/License-LGPL--3.0-blue)

ikik-api is a self-hosted AI API gateway and subscription management platform based on Sub2API. It provides account pooling, API key management, multi-provider request forwarding, usage accounting, subscription billing, moderation controls, and admin operations for AI API services.

English | [中文](README_CN.md) | [日本語](README_JA.md)

Website: [https://ikik.net](https://ikik.net)

QQ group: `146499741`

This repository is intended for private deployment, customization, and secondary development. It does not include production secrets, private server configuration, hosted-service credentials, or commercial operation data.

## Important Notice

Please read the following carefully before deploying or operating this project:

- Terms of service risk: routing requests through subscription or account-based upstreams may violate the terms of service of some upstream providers. Review the relevant provider agreements before use.
- Compliance: use this project only in compliance with the laws and regulations of your country or region.
- Account risk: account bans, quota resets, service interruptions, upstream policy changes, and billing errors are operational risks that must be handled by the deployer.
- Disclaimer: this project is provided for technical learning, self-hosting, and secondary development. You are responsible for your own deployment, data, users, payments, and upstream accounts.

## Features

- OpenAI-compatible gateway endpoints for chat, responses, models, embeddings, image, and streaming workloads.
- Grok OAuth, Kiro OAuth, free-model provider onboarding, and configurable private-account access flows.
- Multi-provider routing for OpenAI-compatible channels and account-based upstreams.
- Account pool management with public, private, owned, and carpool-style scheduling concepts.
- API key management with multi-group routing, IP access controls, quota controls, usage records, and billing metadata.
- User subscriptions, recharge flows, redeem codes, invitation rewards, and shop/card-key workflows.
- Admin dashboard for users, accounts, channels, payments, moderation, risk events, data management, and system settings.
- Content moderation and risk-control integration points for request/response auditing.
- Built-in release workflow for tagged builds, Docker images, archives, and GitHub Releases.
- Frontend console built with Vue 3, TypeScript, Pinia, Vue Router, Tailwind CSS, and Vite.
- Backend service built with Go, Gin, Ent, MariaDB/SQLite, Redis, and modular service boundaries.

## Version 1.0.3 Updates

- Upgraded the backend toolchain to Go 1.27.0 and refreshed vulnerable AWS SDK dependencies used by storage integrations.
- Added Grok OAuth integration, Kiro OAuth integration, K12 account-level support, and video-related gateway endpoint coverage.
- Added free-model provider onboarding, multi-group API key routing, and API Key IP access-control support.
- Improved carpool pool, private account, subscription, billing, reasoning-token, and usage-stat workflows.
- Updated CI, security scanning, and frontend audit handling for the current dependency set.

## Tech Stack

- Backend: Go 1.27.0, Gin, Ent, SQLite/MySQL-compatible databases, Redis
- Frontend: Vue 3, TypeScript, Vite, Pinia, Tailwind CSS
- Testing: Go test, Vitest, vue-tsc, ESLint
- Deployment: Docker or source build; SQLite + Redis by default, with an optional MariaDB stack

## Repository Layout

```text
.
├── backend/              # Go backend, migrations, services, handlers, repositories
├── frontend/             # Vue 3 admin/user console
├── deploy/               # Deployment examples and configuration template
├── docs/                 # Additional integration and operation documents
├── assets/               # Static project assets
├── Makefile              # Common build and test entry points
└── Dockerfile            # Production image build
```

## Requirements

- Go 1.27.0
- Node.js 20+
- pnpm 9+
- SQLite (default) or MariaDB 10.11+
- Redis
- Docker, optional but recommended for deployment

## Configuration

Start from the example configuration:

```bash
cp deploy/config.example.yaml deploy/config.yaml
```

Edit the generated configuration for your environment:

- `server`: host, port, frontend URL, request body limits, CORS, and security headers.
- `database`: MySQL/MariaDB or SQLite connection settings.
- `redis`: cache and queue backend settings.
- `gateway`: upstream timeout, body-size limits, routing, and model behavior.
- `security`: URL allowlist, response header filtering, proxy fallback, and CSP.
- payment, email, storage, moderation, and OAuth sections as needed.

Never commit real production credentials. Local and deployment-specific config files are intentionally ignored by git.

## Development

Install frontend dependencies:

```bash
pnpm --dir frontend install
```

Run the frontend dev server:

```bash
pnpm --dir frontend run dev
```

Run the backend from source:

```bash
cd backend
go run ./cmd/server
```

On first run, the backend may start the setup flow if no valid configuration or installation state is detected.

## Build

Build backend and frontend:

```bash
make build
```

Build only the backend:

```bash
make build-backend
```

Build only the frontend:

```bash
make build-frontend
```

Build a Docker image:

```bash
docker build -t ikik-api:local .
```

## Docker Quick Start

The maintained deployment pulls the multi-platform image `ghcr.io/ipanel/sliderapiv2:latest` and starts the default SQLite + Redis stack:

```bash
mkdir -p sliderapiv2 && cd sliderapiv2
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash
```

SQLite data is persisted under `data/`. New installations open the browser setup wizard by default with SQLite preselected. To deploy the optional bundled MariaDB stack, run `curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash -s -- --database mysql` and choose MySQL in the wizard. The default SQLite Compose stack does not start MariaDB; selecting MySQL there requires an external database reachable from the application container. Changing the driver or Compose file does not migrate existing data. Set `AUTO_SETUP=true` before the first startup only when unattended environment-based initialization is required. For production, pin `IKIK_API_IMAGE` in `.env` to a release tag such as `ghcr.io/ipanel/sliderapiv2:vX.Y.Z`. See [deploy/README.md](deploy/README.md) for manual deployment, database selection, upgrades, backup, OAuth variables, and 404 troubleshooting.

## Tests

Run all configured checks:

```bash
make test
```

Run backend tests:

```bash
cd backend
go test -tags=unit ./...
go test -tags=integration ./...
```

Run frontend checks:

```bash
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run i18n:audit:strict
pnpm --dir frontend exec vitest run
```

Run golangci-lint with the repository configuration:

```bash
cd backend
golangci-lint run ./... --timeout=30m
```

If `golangci-lint` is not installed locally, use the same version as CI:

```bash
cd backend
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.9.0 run ./... --timeout=30m
```

## Deployment Notes

For production, run ikik-api behind a reverse proxy such as Nginx, Caddy, or a managed load balancer.

### Nginx Reverse Proxy Note

When using Nginx with account scheduling, sticky sessions, Codex CLI, or clients that send headers containing underscores, enable underscore headers in the Nginx `http` block:

```nginx
underscores_in_headers on;
```

Nginx drops headers containing underscores by default. That can break session routing and some native client compatibility paths.

Recommended production basics:

- Use SQLite for a single application instance, or MariaDB for multiple replicas/external database operations; keep Redis available.
- Mount a production config file instead of baking secrets into the image.
- Terminate TLS at the reverse proxy or load balancer.
- Keep `/api/*`, `/v1/*`, streaming, and gateway routes out of CDN cache.
- Configure request body limits consistently across the reverse proxy and backend.
- Back up the selected database and `/app/data` before applying migrations or upgrading the application.

## Security

- Do not commit API keys, OAuth secrets, payment keys, database passwords, or server credentials.
- Review `deploy/config.example.yaml` before exposing the service publicly.
- Restrict admin access with strong passwords, MFA where available, and trusted reverse-proxy rules.
- Keep payment, storage, moderation, and email credentials scoped to the minimum required permissions.
- Run `make secret-scan` before publishing changes.

## License

This project follows the license included in [LICENSE](LICENSE).

## Acknowledgements

ikik-api is based on Sub2API and extends it for self-hosted AI gateway, subscription, accounting, and operation workflows.

- [PIXEL-API/PixelAPI](https://github.com/PIXEL-API/PixelAPI)
- [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api)
# SliderAPI
