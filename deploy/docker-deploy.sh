#!/usr/bin/env bash
# =============================================================================
# ikik-api Docker one-click deployment
# =============================================================================
# Downloads the current Compose/environment templates, generates local secrets
# on first install, preserves existing credentials during upgrades, and starts
# the SQLite-first stack (or the optional MariaDB stack).
# =============================================================================

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

GITHUB_REPOSITORY="ipanel/SliderAPIv2"
GITHUB_API_URL="https://api.github.com/repos/${GITHUB_REPOSITORY}"
GITHUB_RAW_URL=""
RELEASE_TAG=""
IMAGE_REF=""
COMPOSE_TMP=".docker-compose.yml.tmp.$$"
ENV_EXAMPLE_TMP=".env.example.tmp.$$"
COMPOSE_CONFIG_TMP=".docker-compose.config.tmp.$$"
ASSUME_YES=false
DATABASE_CHOICE=""
STORAGE_CHOICE=""
COMPOSE_PROJECT=""
COMPOSE_ARGS=()

print_info() { echo -e "${BLUE}[INFO]${NC} $1"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
print_error() { echo -e "${RED}[ERROR]${NC} $1"; }
command_exists() { command -v "$1" >/dev/null 2>&1; }
generate_secret() { openssl rand -hex 32; }
cleanup() { rm -f "$COMPOSE_TMP" "$ENV_EXAMPLE_TMP" "$COMPOSE_CONFIG_TMP"; }

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

read_env_value() {
    local key="$1"
    local value
    value="$(sed -n "s/^${key}=//p" .env | tail -n 1 | tr -d '\r')"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    case "$value" in
        \"*\") value="${value#\"}"; value="${value%\"}" ;;
        \'*\') value="${value#\'}"; value="${value%\'}" ;;
    esac
    printf '%s' "$value"
}

read_env() {
    read_env_value "$1" | tr '[:upper:]' '[:lower:]'
}

resolve_release_tag() {
    local release_json
    if command_exists curl; then
        release_json="$(curl -fsSL -H 'Accept: application/vnd.github+json' "${GITHUB_API_URL}/releases/latest")"
    elif command_exists wget; then
        release_json="$(wget -qO- --header='Accept: application/vnd.github+json' "${GITHUB_API_URL}/releases/latest")"
    else
        print_error "curl or wget is required."
        exit 1
    fi
    RELEASE_TAG="$(printf '%s\n' "$release_json" | sed -nE 's/^[[:space:]]*"tag_name":[[:space:]]*"([^"]+)",?$/\1/p' | head -n 1)"
    if [[ ! "$RELEASE_TAG" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
        print_error "No strict vX.Y.Z tag was found for ${GITHUB_REPOSITORY}."
        exit 1
    fi
    GITHUB_RAW_URL="https://raw.githubusercontent.com/${GITHUB_REPOSITORY}/${RELEASE_TAG}/deploy"
    IMAGE_REF="ghcr.io/$(printf '%s' "$GITHUB_REPOSITORY" | tr '[:upper:]' '[:lower:]'):${RELEASE_TAG}"
}

set_database_choice() {
    local requested="${1,,}"
    case "$requested" in
        sqlite|mysql) ;;
        *) print_error "Unsupported database: $requested (use sqlite or mysql)."; exit 2 ;;
    esac
    if [ -n "$DATABASE_CHOICE" ] && [ "$DATABASE_CHOICE" != "$requested" ]; then
        print_error "Conflicting database options: $DATABASE_CHOICE and $requested."
        exit 2
    fi
    DATABASE_CHOICE="$requested"
}

validate_database_choice() {
    case "$DATABASE_CHOICE" in
        sqlite|mysql) ;;
        *) print_error "Unsupported database: $DATABASE_CHOICE (use sqlite or mysql)."; exit 2 ;;
    esac
}

resolve_database_choice() {
    local configured=""

    if [ -f .env ]; then
        configured="$(read_env DATABASE_DRIVER)"
        if [ -z "$configured" ]; then
            # Legacy official deployments were MariaDB-only. Keep that behavior
            # instead of silently pointing an existing install at a new SQLite DB.
            configured="mysql"
        fi
        if [ -n "$DATABASE_CHOICE" ] && [ "$DATABASE_CHOICE" != "$configured" ]; then
            print_error "Existing deployment uses DATABASE_DRIVER=$configured; refusing to switch it automatically to $DATABASE_CHOICE."
            print_info "Database engine changes require an explicit data migration."
            exit 1
        fi
        DATABASE_CHOICE="$configured"
    elif [ -z "$DATABASE_CHOICE" ]; then
        DATABASE_CHOICE="sqlite"
    fi

    validate_database_choice
}

has_persistent_data() {
    local directory
    local first_entry
    for directory in data mariadb_data redis_data; do
        if [ -e "$directory" ] || [ -L "$directory" ]; then
            if [ ! -d "$directory" ] || [ ! -r "$directory" ]; then
                return 0
            fi
            if ! first_entry="$(find -L "$directory" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)"; then
                return 0
            fi
            if [ -n "$first_entry" ]; then
                return 0
            fi
        fi
    done
    return 1
}


# Detect orphaned containers or named volumes before treating a directory as a new install.
has_existing_docker_resources() {
    local container
    local volume_label

    for container in ikik-api ikik-api-redis ikik-api-mariadb; do
        if docker inspect --type container "$container" >/dev/null 2>&1; then
            return 0
        fi
    done

    for volume_label in ikik_api_data redis_data mariadb_data; do
        if [ -n "$(docker volume ls -q --filter "label=com.docker.compose.volume=$volume_label" 2>/dev/null)" ]; then
            return 0
        fi
    done

    return 1
}

# Resolve an existing path without requiring readlink -f on every target platform.
canonical_existing_path() {
    local path="$1"
    local base_dir="$2"
    local directory
    local filename

    if [[ "$path" != /* ]]; then
        path="${base_dir%/}/$path"
    fi
    directory="$(dirname "$path")"
    filename="$(basename "$path")"
    if [ -d "$directory" ]; then
        directory="$(cd "$directory" && pwd -P)"
    fi
    printf '%s/%s' "${directory%/}" "$filename"
}

# Verify existing containers were created from this directory's managed file.
check_existing_compose_provenance() {
    local expected_dir
    local expected_file
    local container
    local config_files
    local working_dir
    local config_file
    local resolved_file

    expected_dir="$(pwd -P)"
    expected_file="$expected_dir/docker-compose.yml"

    for container in ikik-api ikik-api-redis ikik-api-mariadb; do
        if ! docker inspect --type container "$container" >/dev/null 2>&1; then
            continue
        fi
        config_files="$(docker inspect --type container --format '{{ index .Config.Labels "com.docker.compose.project.config_files" }}' "$container" 2>/dev/null || true)"
        working_dir="$(docker inspect --type container --format '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$container" 2>/dev/null || true)"
        case "$config_files" in
            ""|"<no value>")
                print_error "Cannot verify the Compose file used to create container $container."
                print_info "Update manually with the exact original Compose file list."
                exit 1
                ;;
            *,*)
                print_error "Container $container was created from multiple Compose files: $config_files"
                print_info "The automatic updater only manages a single ./docker-compose.yml file."
                exit 1
                ;;
        esac
        if [ -z "$working_dir" ] || [ "$working_dir" = "<no value>" ]; then
            print_error "Cannot verify the Compose working directory for container $container."
            print_info "Update manually with the exact original Compose file list."
            exit 1
        fi
        config_file="${config_files#file://}"
        resolved_file="$(canonical_existing_path "$config_file" "$working_dir")"
        if [ "$resolved_file" != "$expected_file" ]; then
            print_error "Container $container was created from $resolved_file, not $expected_file."
            print_info "Run Docker Compose manually with the original -f file to avoid attaching the wrong data."
            exit 1
        fi
    done
}

# Reject Compose inputs that cannot be reproduced safely by this updater.
check_unsupported_compose_inputs() {
    local env_compose_file=""
    local override_file
    local alternate_file

    if [ -f .env ]; then
        env_compose_file="$(read_env_value COMPOSE_FILE)"
    fi
    if [ -n "${COMPOSE_FILE:-}" ] || [ -n "$env_compose_file" ]; then
        print_error "Existing COMPOSE_FILE configuration is not supported by the automatic updater."
        print_info "Run Docker Compose manually with the same complete file list used by the deployment."
        exit 1
    fi

    for override_file in compose.override.yaml compose.override.yml docker-compose.override.yaml docker-compose.override.yml; do
        if [ -e "$override_file" ] || [ -L "$override_file" ]; then
            print_error "Detected $override_file. The automatic updater will not ignore an existing Compose override."
            print_info "Run Docker Compose manually with the original base and override files."
            exit 1
        fi
    done

    # A managed one-click directory contains only the selected stack renamed to
    # docker-compose.yml. Reject any other YAML file that looks like a Compose
    # stack, including custom names such as production.yml.
    for alternate_file in ./*.yml ./*.yaml; do
        [ -e "$alternate_file" ] || [ -L "$alternate_file" ] || continue
        [ "$alternate_file" = "./docker-compose.yml" ] && continue
        if grep -Eq '^[[:space:]]*services:[[:space:]]*($|#)' "$alternate_file"; then
            print_error "Detected alternate Compose file $alternate_file next to docker-compose.yml."
            print_info "This updater only manages installations where the selected stack is saved as ./docker-compose.yml."
            print_info "For a manual -f deployment, update with the exact original Compose file instead."
            exit 1
        fi
    done

    check_existing_compose_provenance
}

# Legacy runtime connection variables override config.yaml on every restart.
check_legacy_runtime_overrides() {
    if ! docker compose -f ./docker-compose.yml config --no-interpolate > "$COMPOSE_CONFIG_TMP"; then
        print_error "Cannot render the existing Docker Compose configuration."
        exit 1
    fi
    chmod 600 "$COMPOSE_CONFIG_TMP"
    if awk '
        /^  ikik-api:$/ { in_app=1; in_env=0; next }
        in_app && /^  [^[:space:]][^:]*:$/ { in_app=0; in_env=0 }
        in_app && /^    environment:$/ { in_env=1; next }
        in_env && /^    [^[:space:]][^:]*:/ { in_env=0 }
        in_env && /^      (DATABASE_(DRIVER|PATH|HOST|PORT|USER|PASSWORD|DBNAME|SSLMODE)|REDIS_(HOST|PORT|PASSWORD|DB|ENABLE_TLS)):/ { found=1 }
        END { exit(found ? 0 : 1) }
    ' "$COMPOSE_CONFIG_TMP"; then
        print_error "Legacy DATABASE_* or REDIS_* connection overrides were found on the ikik-api service."
        print_info "They can override /app/data/config.yaml after every restart, so this updater will not continue automatically."
        print_info "Rename only the ikik-api connection entries to SETUP_DATABASE_* and SETUP_REDIS_*, then rerun the updater."
        print_info "Keep pool/tuning variables and the Redis service's own REDIS_PASSWORD unchanged."
        exit 1
    fi
}

# Discover prior Compose project names from existing container and volume labels.
discover_compose_projects() {
    local container
    local volume
    local volume_label

    {
        for container in ikik-api ikik-api-redis ikik-api-mariadb; do
            docker inspect --type container --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$container" 2>/dev/null || true
        done
        for volume_label in ikik_api_data redis_data mariadb_data; do
            while IFS= read -r volume; do
                [ -n "$volume" ] || continue
                docker volume inspect --format '{{ index .Labels "com.docker.compose.project" }}' "$volume" 2>/dev/null || true
            done < <(docker volume ls -q --filter "label=com.docker.compose.volume=$volume_label" 2>/dev/null)
        done
    } | sed '/^$/d; /^<no value>$/d' | sort -u
}

# Resolve a stable Compose project name without guessing for named/custom storage.
resolve_compose_project() {
    local shell_project="${COMPOSE_PROJECT_NAME:-}"
    local env_project=""
    local -a detected_projects=()

    if [ -f .env ]; then
        env_project="$(read_env_value COMPOSE_PROJECT_NAME)"
    fi
    if [ -n "$shell_project" ] && [ -n "$env_project" ] && [ "$shell_project" != "$env_project" ]; then
        print_error "COMPOSE_PROJECT_NAME differs between the shell and .env."
        print_info "Use one stable project name before updating to avoid attaching different named volumes."
        exit 1
    fi
    if [ -n "$shell_project" ]; then
        COMPOSE_PROJECT="$shell_project"
        return
    fi
    if [ -n "$env_project" ]; then
        COMPOSE_PROJECT="$env_project"
        return
    fi

    if [ -f .env ]; then
        mapfile -t detected_projects < <(discover_compose_projects)
        if [ "${#detected_projects[@]}" -gt 1 ]; then
            print_error "Multiple existing Compose project names were detected: ${detected_projects[*]}"
            print_info "Set COMPOSE_PROJECT_NAME in .env to the project that belongs to this deployment."
            exit 1
        fi
        if [ "${#detected_projects[@]}" -eq 1 ]; then
            COMPOSE_PROJECT="${detected_projects[0]}"
            return
        fi
        if [ "$STORAGE_CHOICE" = "named" ] || [ "$STORAGE_CHOICE" = "custom" ]; then
            print_error "Cannot determine the existing Compose project name safely."
            print_info "Set COMPOSE_PROJECT_NAME in .env to the original value before updating."
            exit 1
        fi
        return
    fi

    COMPOSE_PROJECT="sliderapiv2"
}

# Build one Compose argument set reused for validation, pull, and startup.
build_compose_args() {
    COMPOSE_ARGS=(-f ./docker-compose.yml)
    if [ -n "$COMPOSE_PROJECT" ]; then
        COMPOSE_ARGS=(--project-name "$COMPOSE_PROJECT" -f ./docker-compose.yml)
    fi
}

# Classify storage only for template selection and upgrade safety checks.
resolve_storage_choice() {
    local detected=""

    if [ -f docker-compose.yml ]; then
        if grep -Eq '^[[:space:]]*-[[:space:]]+\./data:/app/data([[:space:]]|$)' docker-compose.yml; then
            detected="local"
        elif grep -Eq '^[[:space:]]*-[[:space:]]+ikik_api_data:/app/data([[:space:]]|$)' docker-compose.yml; then
            detected="named"
        else
            # Existing deployments keep their Compose file unchanged, so custom
            # bind mounts and volume names are safe to preserve during upgrades.
            detected="custom"
        fi
    elif [ -f .env ]; then
        print_error "Existing .env found but docker-compose.yml is missing."
        print_info "Restore the original Compose file before running the updater."
        exit 1
    else
        detected="local"
    fi

    STORAGE_CHOICE="$detected"
}

# Download the complete Compose stack selected for a new installation.
download_templates() {
    local compose_template="docker-compose.local.yml"
    if [ "$STORAGE_CHOICE" = "named" ]; then
        compose_template="docker-compose.yml"
    fi
    if [ "$DATABASE_CHOICE" = "mysql" ]; then
        if [ "$STORAGE_CHOICE" = "named" ]; then
            compose_template="docker-compose.mysql.yml"
        else
            compose_template="docker-compose.local.mysql.yml"
        fi
    fi

    if command_exists curl; then
        curl -fsSL "${GITHUB_RAW_URL}/${compose_template}" -o "$COMPOSE_TMP"
        curl -fsSL "${GITHUB_RAW_URL}/.env.example" -o "$ENV_EXAMPLE_TMP"
    elif command_exists wget; then
        wget -q "${GITHUB_RAW_URL}/${compose_template}" -O "$COMPOSE_TMP"
        wget -q "${GITHUB_RAW_URL}/.env.example" -O "$ENV_EXAMPLE_TMP"
    else
        print_error "curl or wget is required."
        exit 1
    fi
}

# Render Compose once and ensure the application will run the pinned release image.
validate_compose_image() {
    local resolved_image
    if ! docker compose "${COMPOSE_ARGS[@]}" config > "$COMPOSE_CONFIG_TMP"; then
        print_error "Cannot render the Docker Compose configuration."
        exit 1
    fi
    chmod 600 "$COMPOSE_CONFIG_TMP"
    resolved_image="$(awk '
        /^  ikik-api:$/ { in_app=1; next }
        in_app && /^  [^[:space:]][^:]*:$/ { in_app=0 }
        in_app && /^    image:/ {
            sub(/^[[:space:]]*image:[[:space:]]*/, "")
            print
            exit
        }
    ' "$COMPOSE_CONFIG_TMP")"
    resolved_image="${resolved_image#\"}"
    resolved_image="${resolved_image%\"}"
    resolved_image="${resolved_image#\'}"
    resolved_image="${resolved_image%\'}"
    if [ -z "$resolved_image" ]; then
        print_error "The rendered Compose configuration has no image for the ikik-api service."
        exit 1
    fi
    if [ "$resolved_image" != "$IMAGE_REF" ]; then
        print_error "The existing Compose file resolves ikik-api.image to $resolved_image instead of $IMAGE_REF."
        print_info "Ensure the ikik-api image uses \${IKIK_API_IMAGE} and remove any shell override for IKIK_API_IMAGE before retrying."
        exit 1
    fi
}

# Parse database selection and unattended confirmation flags.
parse_args() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            -y|--yes) ASSUME_YES=true ;;
            --sqlite) set_database_choice sqlite ;;
            --mysql) set_database_choice mysql ;;
            --database)
                [ "$#" -ge 2 ] || { print_error "--database requires sqlite or mysql."; exit 2; }
                set_database_choice "$2"
                shift
                ;;
            --database=*) set_database_choice "${1#*=}" ;;
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

    command_exists docker || { print_error "Docker is required."; exit 1; }
    docker compose version >/dev/null 2>&1 || { print_error "Docker Compose v2 (docker compose) is required."; exit 1; }
    resolve_release_tag

    if [ -f .env ]; then
        existing_env=true
        print_warning "Existing deployment detected. The updater will preserve .env and all persistent credentials."
        if [ ! -f docker-compose.yml ]; then
            print_error "Existing .env found but docker-compose.yml is missing."
            print_info "Restore the original Compose file before running the updater."
            exit 1
        fi
    elif [ -e docker-compose.yml ] || [ -L docker-compose.yml ]; then
        print_error "docker-compose.yml exists but .env is missing. Refusing to generate replacement credentials."
        print_info "Restore the original .env, or move the old deployment files out of this directory for a clean install."
        exit 1
    elif has_persistent_data; then
        print_error "Persistent data exists but .env is missing. Refusing to generate replacement credentials."
        print_info "Restore the original .env before updating this deployment."
        exit 1
    elif has_existing_docker_resources; then
        print_error "Existing ikik-api Docker containers or named volumes were found, but deployment files are missing."
        print_info "Restore the original .env and docker-compose.yml instead of generating replacement credentials."
        exit 1
    fi

    resolve_database_choice
    resolve_storage_choice
    if [ "$existing_env" = true ]; then
        check_unsupported_compose_inputs
        check_legacy_runtime_overrides
    fi
    resolve_compose_project
    build_compose_args
    print_info "Database mode: $DATABASE_CHOICE"
    print_info "Storage mode: $STORAGE_CHOICE"
    print_info "Release: $RELEASE_TAG"

    if [ "$existing_env" = true ]; then
        print_warning "Existing deployment detected. The current docker-compose.yml and .env will be preserved."
        if [ "$ASSUME_YES" != true ]; then
            if [ ! -t 0 ] && [ ! -r /dev/tty ]; then
                print_error "Cannot confirm update in non-interactive mode. Re-run with --yes after creating a backup."
                exit 1
            fi
            read -r -p "Pull the latest image and recreate services with the existing Compose file? (y/N): " reply </dev/tty
            if [[ ! "$reply" =~ ^[Yy]$ ]]; then
                print_info "Cancelled."
                exit 0
            fi
        fi
        print_success "Preserved the existing docker-compose.yml and credentials; the resolved Compose project and volume mappings will be reused."
        replace_env IKIK_API_IMAGE "$IMAGE_REF"
    else
        command_exists openssl || { print_error "openssl is required for generating new deployment secrets."; exit 1; }
        print_info "Downloading the $DATABASE_CHOICE/$STORAGE_CHOICE Docker Compose template and environment template..."
        download_templates
        mv "$COMPOSE_TMP" docker-compose.yml
        mv "$ENV_EXAMPLE_TMP" .env.example
        cp .env.example .env
        replace_env DATABASE_DRIVER "$DATABASE_CHOICE"
        replace_env COMPOSE_PROJECT_NAME "$COMPOSE_PROJECT"
        replace_env IKIK_API_IMAGE "$IMAGE_REF"
        if [ "$DATABASE_CHOICE" = "mysql" ]; then
            replace_env MARIADB_ROOT_PASSWORD "$(generate_secret)"
            replace_env MARIADB_PASSWORD "$(generate_secret)"
        fi
        replace_env REDIS_PASSWORD "$(generate_secret)"
        replace_env JWT_SECRET "$(generate_secret)"
        replace_env TOTP_ENCRYPTION_KEY "$(generate_secret)"
        print_success "Generated credentials for the new deployment."
    fi
    chmod 600 .env

    if [ "$STORAGE_CHOICE" = "local" ]; then
        mkdir -p data redis_data
        if [ "$DATABASE_CHOICE" = "mysql" ]; then
            mkdir -p mariadb_data
        fi
    fi

    print_info "Validating Docker Compose configuration and pinned image..."
    validate_compose_image
    print_info "Pulling multi-architecture images..."
    docker compose "${COMPOSE_ARGS[@]}" pull
    print_info "Starting services..."
    docker compose "${COMPOSE_ARGS[@]}" up -d

    echo
    print_success "Deployment started with $DATABASE_CHOICE."
    echo "  Web UI:      http://localhost:8080"
    if [ -n "$COMPOSE_PROJECT" ]; then
        echo "  Project:     $COMPOSE_PROJECT"
        echo "  Status:      docker compose --project-name $COMPOSE_PROJECT -f ./docker-compose.yml ps"
        echo "  App logs:    docker compose --project-name $COMPOSE_PROJECT -f ./docker-compose.yml logs -f ikik-api"
        echo "  Upgrade:     docker compose --project-name $COMPOSE_PROJECT -f ./docker-compose.yml pull && docker compose --project-name $COMPOSE_PROJECT -f ./docker-compose.yml up -d"
    else
        echo "  Status:      docker compose -f ./docker-compose.yml ps"
        echo "  App logs:    docker compose -f ./docker-compose.yml logs -f ikik-api"
        echo "  Upgrade:     docker compose -f ./docker-compose.yml pull && docker compose -f ./docker-compose.yml up -d"
    fi
    echo
    print_warning "Secrets are stored only in .env and are not printed. Back up .env and persistent data securely."
    case "$(read_env AUTO_SETUP)" in
        true|1|yes)
            print_info "Unattended setup is enabled. If ADMIN_PASSWORD is empty, read the generated admin password from the first-start logs."
            ;;
        *)
            print_info "Open the Web UI to complete first-run setup; SQLite is selected by default."
            print_info "Use Redis host 'redis' and the REDIS_PASSWORD stored in .env."
            if [ "$DATABASE_CHOICE" = "mysql" ]; then
                print_info "Choose MySQL in the wizard and use host 'mariadb' plus the MARIADB_USER/MARIADB_PASSWORD values from .env."
            fi
            ;;
    esac
}

main "$@"