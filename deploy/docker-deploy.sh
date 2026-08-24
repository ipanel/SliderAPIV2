#!/usr/bin/env bash
# =============================================================================
# ikik-api Docker one-click deployment
# =============================================================================
# Downloads the current Compose/environment templates, generates local secrets
# on first install, preserves existing credentials during upgrades, and starts
# MariaDB, Redis, and ghcr.io/ipanel/sliderapiv2.
# =============================================================================

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

GITHUB_RAW_URL="https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy"
COMPOSE_TMP=".docker-compose.yml.tmp.$$"
ENV_EXAMPLE_TMP=".env.example.tmp.$$"
ASSUME_YES=false

print_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
print_error() { echo -e "${RED}[ERROR]${NC} $1"; }
command_exists() { command -v "$1" >/dev/null 2>&1; }
generate_secret() { openssl rand -hex 32; }
cleanup() { rm -f "$COMPOSE_TMP" "$ENV_EXAMPLE_TMP"; }

replace_env() {
    local key="$1"
    local value="$2"
    local temp_file=".env.tmp.$$"
    awk -v key="$key" -v value="$value" '
        BEGIN { replaced = 0 }
        index($0, key "=") == 1 { print key "=" value; replaced = 1; next }
        { print }
        END { if (!replaced) print key "=" value }
    ' .env > "$temp_file"
    mv "$temp_file" .env
}

validate_existing_env() {
    local missing=()
    local key
    local value

    # Compose requires these values even when an existing MariaDB volume is reused.
    for key in MARIADB_ROOT_PASSWORD MARIADB_PASSWORD; do
        value="$(sed -n "s/^${key}=//p" .env | tail -n 1)"
        if [ -z "$value" ]; then
            missing+=("$key")
        fi
    done

    if [ "${#missing[@]}" -gt 0 ]; then
        print_error "Existing .env is missing required MariaDB settings: ${missing[*]}"
        print_error "The updater will not replace credentials or auto-migrate a legacy database."
        print_info "Back up the deployment, migrate the database explicitly, then add the required values to .env."
        exit 1
    fi
}

has_persistent_data() {
    local directory
    for directory in data mariadb_data redis_data; do
        if [ -d "$directory" ] && [ -n "$(find "$directory" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]; then
            return 0
        fi
    done
    return 1
}

download_templates() {
    if command_exists curl; then
        curl -fsSL "${GITHUB_RAW_URL}/docker-compose.local.yml" -o "$COMPOSE_TMP"
        curl -fsSL "${GITHUB_RAW_URL}/.env.example" -o "$ENV_EXAMPLE_TMP"
    elif command_exists wget; then
        wget -q "${GITHUB_RAW_URL}/docker-compose.local.yml" -O "$COMPOSE_TMP"
        wget -q "${GITHUB_RAW_URL}/.env.example" -O "$ENV_EXAMPLE_TMP"
    else
        print_error "curl or wget is required."
        exit 1
    fi
}

parse_args() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            -y|--yes) ASSUME_YES=true ;;
            *) print_error "Unknown argument: $1"; exit 2 ;;
        esac
        shift
    done
}

main() {
    local existing_env=false

    parse_args "$@"
    trap cleanup EXIT

    echo
    echo "=========================================="
    echo "  ikik-api Docker Deployment"
    echo "=========================================="
    echo

    command_exists openssl || { print_error "openssl is required."; exit 1; }
    command_exists docker || { print_error "Docker is required."; exit 1; }
    docker compose version >/dev/null 2>&1 || { print_error "Docker Compose v2 (docker compose) is required."; exit 1; }

    if [ -f .env ]; then
        existing_env=true
        print_warning "Existing deployment detected. The updater will preserve .env and all persistent credentials."
        validate_existing_env
    elif has_persistent_data; then
        print_error "Persistent data exists but .env is missing. Refusing to generate replacement credentials."
        print_info "Restore the original .env before updating this deployment."
        exit 1
    fi

    if [ -f docker-compose.yml ] || [ "$existing_env" = true ]; then
        print_warning "Deployment files already exist in the current directory."
        if [ "$ASSUME_YES" != true ]; then
            if [ ! -t 0 ] && [ ! -r /dev/tty ]; then
                print_error "Cannot confirm update in non-interactive mode. Re-run with --yes after creating a backup."
                exit 1
            fi
            read -r -p "Update deployment templates? Existing .env will be preserved. (y/N): " reply </dev/tty
            if [[ ! "$reply" =~ ^[Yy]$ ]]; then
                print_info "Cancelled."
                exit 0
            fi
        fi
    fi

    print_info "Downloading Docker Compose and environment templates..."
    download_templates
    mv "$COMPOSE_TMP" docker-compose.yml
    mv "$ENV_EXAMPLE_TMP" .env.example

    if [ "$existing_env" = false ]; then
        cp .env.example .env
        replace_env MARIADB_ROOT_PASSWORD "$(generate_secret)"
        replace_env MARIADB_PASSWORD "$(generate_secret)"
        replace_env REDIS_PASSWORD "$(generate_secret)"
        replace_env JWT_SECRET "$(generate_secret)"
        replace_env TOTP_ENCRYPTION_KEY "$(generate_secret)"
        print_success "Generated credentials for the new deployment."
    else
        print_success "Preserved the existing .env credentials."
    fi
    chmod 600 .env

    mkdir -p data mariadb_data redis_data

    print_info "Pulling multi-architecture images..."
    docker compose pull
    print_info "Starting services..."
    docker compose up -d

    echo
    print_success "Deployment started."
    echo "  Web UI:      http://localhost:8080"
    echo "  Status:      docker compose ps"
    echo "  App logs:    docker compose logs -f ikik-api"
    echo "  Upgrade:     docker compose pull && docker compose up -d"
    echo
    print_warning "Secrets are stored only in .env and are not printed. Back up .env and data directories securely."
    print_info "If ADMIN_PASSWORD is empty, read the generated admin password from the first-start logs."
}

main "$@"