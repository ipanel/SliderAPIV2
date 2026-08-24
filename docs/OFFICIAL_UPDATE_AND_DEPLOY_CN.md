# 官方版本合并与自定义镜像部署流程

本文用于维护当前二开版本：在后续官方 `wenyi401/ikik-api` 发布新版本时，合并官方更新，同时保留本项目的个人账号、公共账号共享、分成结算、收益管理增强、凭证导入限制等二开功能。

## 当前基线

当前仓库地址：

```text
https://github.com/ipanel/SliderAPIv2
```

长期开发与部署分支为 `main`，发布版本使用严格格式的 `vX.Y.Z` tag。不要在文档中固定某个历史版本作为永久基线；开始升级前应从当前仓库和官方上游获取最新状态。

首次配置远端时：

```bash
git remote set-url origin https://github.com/ipanel/SliderAPIv2.git
git remote add upstream https://github.com/wenyi401/ikik-api.git
git fetch origin --prune
git fetch upstream --tags --prune
```

如果 `upstream` 已存在，改用：

```bash
git remote set-url upstream https://github.com/wenyi401/ikik-api.git
git fetch upstream --tags --prune
```

建议开启 Git 冲突复用记录：

```bash
git config rerere.enabled true
```

## 每次合并官方新版本

下面使用 `TARGET_TAG` 表示准备合并的官方严格版本标签，例如 `v1.2.3`。先明确设置目标版本，再执行后续命令。

### 1. 确认工作区干净

```bash
git switch main
git status --short
```

必须看到没有未提交改动后再继续。若有本地改动，先提交或另建分支保存。

### 2. 拉取官方 tag

```bash
git fetch upstream --tags --prune
```

查看最近版本：

```bash
git tag -l "v*" --sort=-version:refname | head
```

Windows PowerShell 可用：

```powershell
git tag -l "v*" --sort=-version:refname | Select-Object -First 10
```

如果只想拉取指定 tag：

```bash
TARGET_TAG=vX.Y.Z
git fetch upstream "refs/tags/${TARGET_TAG}:refs/tags/${TARGET_TAG}"
```

### 3. 创建升级分支

```bash
git switch main
git pull --ff-only origin main
git switch -c "upgrade/${TARGET_TAG}-merge"
```

如果没有远程私有仓库，`git pull --ff-only` 可以跳过。

### 4. 合并官方版本

```bash
git merge --no-ff "$TARGET_TAG"
```

如果没有冲突，直接进入验证步骤。

如果出现冲突，先查看冲突文件：

```bash
git status --short
git diff --name-only --diff-filter=U
```

### 5. 冲突处理原则

处理冲突时优先保留二开业务边界：

- 普通用户账号必须继续按 `owner_user_id`、`share_mode`、`share_status` 做后端隔离。
- 公共账号分组只能作为调度池，不能成为权限边界。
- 普通用户不能通过 API Key、Upstream、URL、Base URL 添加账号。
- 凭证导入必须继续拒绝 API Key-like、URL/base_url/upstream/endpoint/host/proxy_url、cookie、authorization、AWS key 等敏感格式。
- 账号主自己调用自己的公共账号不能产生分成收入。
- 分成流水必须保持幂等、可审计。

历史合并过程中出现过冲突的高频文件：

```bash
backend/cmd/server/wire_gen.go
backend/internal/service/wire.go
backend/internal/service/openai_gateway_service.go
backend/internal/service/setting_service.go
frontend/src/components/account/CreateAccountModal.vue
frontend/src/types/index.ts
frontend/src/views/admin/AccountsView.vue
```

如果官方再次修改这些文件，重点检查：

- `wire.go` 和 `wire_gen.go`：官方新增依赖时，不要覆盖二开的账号共享、分成策略、凭证导入相关依赖注入。
- `openai_gateway_service.go`：不要丢失 `AccountSharePolicyRepository`、公共账号结算、自用排除逻辑。
- `setting_service.go`：不要丢失用户私有分组、分成策略、收益管理相关设置默认值。
- `CreateAccountModal.vue`：官方新增账号类型时，普通用户入口仍必须受 `isUserScope` 限制。
- `types/index.ts`：官方新增枚举时，要和二开的 `share_mode/share_status` 类型一起保留。
- `AccountsView.vue`：官方账号管理增强不能覆盖二开的凭证导入、归属/共享列、刷新和筛选逻辑。

处理完冲突后检查是否还有冲突标记：

```bash
rg -n "^(<<<<<<< .+|=======$|>>>>>>> .+)" backend frontend
git diff --name-only --diff-filter=U
```

如果改到了 Wire 注入关系，重新生成 Wire：

```bash
cd backend
go generate ./cmd/server
cd ..
```

格式化 Go 文件：

```bash
gofmt -w backend/cmd/server/wire_gen.go backend/internal/service/*.go backend/internal/handler/**/*.go backend/internal/repository/*.go
```

如果 PowerShell 对 `**` 展开不符合预期，可以只格式化实际改动的 `.go` 文件。

### 6. 验证

后端：

```bash
cd backend
go test ./...
cd ..
```

前端：

```bash
cd frontend
pnpm install --frozen-lockfile
pnpm run typecheck
cd ..
```

建议再检查一次缓存区和冲突标记：

```bash
git diff --check
rg -n "^(<<<<<<< .+|=======$|>>>>>>> .+)" backend frontend
```

如果合并已暂存，使用：

```bash
git diff --cached --check
```

### 7. 提交合并

```bash
git status
git add <已解决的文件>
git commit -m "chore: merge upstream ${TARGET_TAG}"
```

合并完成后，把升级分支合回长期二开主线：

```bash
git switch main
git merge --ff-only "upgrade/${TARGET_TAG}-merge"
git push origin main
```

如果不允许直接推送 `main`，请推送升级分支并通过 Pull Request 合并。

## 合并后的重点人工检查

自动测试通过后，建议在测试环境做以下人工检查：

- 管理员账号管理：平台账号、用户私有、用户公共、校验状态、归属/共享列展示正常。
- 普通用户我的账号：只能看到自己的账号，不能编辑或删除他人账号。
- 普通用户新增账号：页面上只能走 OAuth 或凭证导入，后端也拒绝 API Key、Upstream、URL/Base URL。
- 凭证导入：用户接口和管理员接口都能识别合法凭证，能拒绝敏感字段。
- 公共账号调用：别人调用用户公共账号时生成分成，账号主自用时不生成分成。
- 收益管理：消费用户、账号主收益、分组/账号/模型的金额方向不混淆。

测试环境可以写入测试数据；生产数据库不要直接手工新增、修改、删除数据。

## 自定义镜像构建

生产环境镜像统一使用 `ghcr.io/ipanel/sliderapiv2:<版本标签>`。不要使用没有仓库限定的 `ikik-api:latest`，也不要只依赖 `latest`；正式部署应固定到可回滚的版本标签。

根目录 `Dockerfile` 是推荐的生产镜像构建入口，会完成：

- 前端 `pnpm run build`
- 后端 `go build -tags embed`
- 前端产物嵌入后端
- 最终生成运行镜像

### 本机单架构构建

Linux/macOS：

```bash
VERSION=vX.Y.Z-custom.1
COMMIT=$(git rev-parse --short HEAD)
IMAGE=ghcr.io/ipanel/sliderapiv2:$VERSION

docker build \
  --build-arg VERSION=$VERSION \
  --build-arg COMMIT=$COMMIT \
  -t $IMAGE \
  -t ghcr.io/ipanel/sliderapiv2:latest \
  .
```

Windows PowerShell：

```powershell
$version = "vX.Y.Z-custom.1"
$commit = git rev-parse --short HEAD
$image = "ghcr.io/ipanel/sliderapiv2:$version"

docker build `
  --build-arg VERSION=$version `
  --build-arg COMMIT=$commit `
  -t $image `
  -t ghcr.io/ipanel/sliderapiv2:latest `
  .
```

### 多架构构建并推送

如果服务器可能是 `amd64` 或 `arm64`，使用 `buildx`：

```bash
VERSION=vX.Y.Z-custom.1
COMMIT=$(git rev-parse --short HEAD)
IMAGE=ghcr.io/ipanel/sliderapiv2:$VERSION

docker buildx create --use --name ikik-api-builder || true
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=$VERSION \
  --build-arg COMMIT=$COMMIT \
  -t $IMAGE \
  -t ghcr.io/ipanel/sliderapiv2:latest \
  --push \
  .
```

如果只在当前机器构建并推送单架构镜像：

```bash
docker login ghcr.io
docker push ghcr.io/ipanel/sliderapiv2:vX.Y.Z-custom.1
docker push ghcr.io/ipanel/sliderapiv2:latest
```

镜像 tag 建议包含官方版本和二开构建序号，例如：

```bash
vX.Y.Z-custom.1
vX.Y.Z-custom.2
```

不要只依赖 `latest`，否则回滚时无法准确定位版本。

## 服务器部署方式

Docker 部署默认使用 SQLite，并保留 MariaDB 作为可选方案：

| 数据库 | 推荐 Compose | 适用场景 |
|---|---|---|
| SQLite（默认） | `deploy/docker-compose.local.yml` | 单实例、自托管、部署维护简单 |
| MariaDB | `deploy/docker-compose.local.mysql.yml` | 多实例、外部数据库、需要数据库运维工具 |

两份 Compose 都是完整配置，不能作为 base/override 叠加使用。SQLite 栈启动 `ikik-api + Redis`，MariaDB 栈启动 `ikik-api + MariaDB + Redis`。

本地目录版将持久数据放在部署目录：

- SQLite：`data/`、`redis_data/`
- MariaDB：`data/`、`mariadb_data/`、`redis_data/`

若使用命名卷，可分别选择 `docker-compose.yml` 或 `docker-compose.mysql.yml`。

## 首次部署自定义镜像

服务器目录示例：

```bash
mkdir -p /opt/ikik-api
cd /opt/ikik-api
```

### 方式一：一键部署 SQLite（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash
```

脚本默认选择 SQLite。显式写法为：

```bash
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash -s -- --database sqlite
```

### 方式二：一键部署 MariaDB

```bash
curl -fsSL https://raw.githubusercontent.com/ipanel/SliderAPIv2/main/deploy/docker-deploy.sh | bash -s -- --database mysql
```

脚本首次安装时创建 `.env`、生成固定密钥并启动对应完整栈，然后通过网页初始化向导完成配置；SQLite 默认选中。新安装默认只把宿主机端口绑定到 `127.0.0.1`，避免未认证的初始化页面被公网或局域网用户抢先访问；请先在本机、SSH 隧道或受保护的反向代理后完成初始化，确认访问控制后再按需设置 `BIND_HOST=0.0.0.0`。升级时保留现有 `.env`、数据库类型、Compose 文件及其中的自定义卷映射。旧部署的 `.env` 若没有 `DATABASE_DRIVER`，会按历史行为保守识别为 MySQL，避免静默连接到空 SQLite。

已有部署不能仅通过 `--database` 或修改 `DATABASE_DRIVER` 切换数据库。脚本发现参数与现有 `.env` 冲突时会拒绝继续，数据库切换必须先完成明确的数据迁移。

新安装会在 `.env` 中固定 `COMPOSE_PROJECT_NAME=sliderapiv2`，避免目录改名后命名卷指向另一组空数据。旧命名卷部署会从已有容器/卷标签识别原项目名；无法可靠识别时脚本会停止，并要求先在 `.env` 中恢复原始 `COMPOSE_PROJECT_NAME`。一键更新还会核对现有容器记录的 Compose 工作目录和配置文件；若部署使用 `COMPOSE_FILE`、标准 override、任意其他 YAML Compose 文件或手动 `-f` 入口，脚本会主动拒绝执行，请使用原有完整 Compose 文件列表手动更新，避免遗漏端口、网络、环境变量或卷映射。

旧版 Compose 若仍在 `ikik-api` 服务中注入运行期 `DATABASE_*` / `REDIS_*` 连接变量，一键更新也会在重建容器前停止。请仅把连接项改名为 `SETUP_DATABASE_*` / `SETUP_REDIS_*`；不要改名 `DATABASE_MAX_*`、`REDIS_POOL_*` 等调优变量，也要保留 Redis 服务自身的 `REDIS_PASSWORD`。同一连接项的 `SETUP_*` 与旧变量即使值相同也不能同时存在，应用会在启动前明确报错。完成这次迁移后，重启将不再用环境变量覆盖 `/app/data/config.yaml`。

### 方式三：手动部署

从仓库复制以下文件：

```text
deploy/.env.example
deploy/docker-compose.local.yml
```

SQLite 使用 `docker-compose.local.yml`；MariaDB 则复制并使用 `docker-compose.local.mysql.yml`。然后：

```bash
cp .env.example .env
chmod 600 .env
```

SQLite 至少确认：

```dotenv
AUTO_SETUP=false
DATABASE_DRIVER=sqlite
DATABASE_PATH=ikik-api.db
IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:vX.Y.Z
REDIS_PASSWORD=<固定随机密钥>
JWT_SECRET=<固定 64 位十六进制密钥>
TOTP_ENCRYPTION_KEY=<固定 64 位十六进制密钥>
```

MariaDB 还必须设置：

```dotenv
AUTO_SETUP=false
DATABASE_DRIVER=mysql
MARIADB_ROOT_PASSWORD=<数据库 root 强密码>
MARIADB_PASSWORD=<应用数据库用户强密码>
```

生成密钥示例：

```bash
openssl rand -hex 32
```

SQLite 启动：

```bash
mkdir -p data redis_data
docker login ghcr.io
docker compose -f docker-compose.local.yml pull
docker compose -f docker-compose.local.yml up -d
```

MariaDB 启动：

```bash
mkdir -p data mariadb_data redis_data
docker login ghcr.io
docker compose -f docker-compose.local.mysql.yml pull
docker compose -f docker-compose.local.mysql.yml up -d
```

生产环境应在 `.env` 中固定 `IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:vX.Y.Z`，不要只依赖 `latest`。

### 使用网页初始化向导

新部署默认 `AUTO_SETUP=false`，启动后打开 `http://127.0.0.1:8080` 完成初始化。向导默认选择 SQLite，同时支持 MySQL。

数据库选择必须与已启动的基础设施匹配：

- SQLite Compose 启动 `ikik-api + Redis`；保持 SQLite，Redis 主机填写 `redis`，密码使用 `.env` 中的 `REDIS_PASSWORD`。
- `*.mysql.yml` 额外启动 MariaDB；选择 MySQL，数据库主机填写 `mariadb`，用户和密码使用 `.env` 中的 `MARIADB_USER`、`MARIADB_PASSWORD`。
- 外部 MySQL/MariaDB 必须确保容器网络可达。

若明确需要无人值守初始化，可在首次启动前设置 `AUTO_SETUP=true`，此时程序直接使用 `.env` 中的 `DATABASE_DRIVER`、管理员和连接配置，不显示网页向导。

初始化结果会写入 `/app/data/config.yaml` 和 `/app/data/.installed`，因此 `data/` 必须持久化。网页向导是“首次安装配置”，不是数据迁移工具；已经初始化后不能通过重新选择数据库来自动迁移数据。

健康检查：

```bash
curl -fsS http://127.0.0.1:8080/health
```

如果 `.env` 中修改了 `SERVER_PORT`，把 `8080` 替换成实际端口。

## 已部署环境更新镜像

以下流程用于把服务器从旧自定义镜像更新到新自定义镜像。升级时继续使用当前部署的同一份 Compose，不要顺手切换数据库类型。

### 1. 备份 SQLite 部署

下面的容器复制方式同时适用于本地目录和 Docker 命名卷。为了让 SQLite 主文件、WAL、共享内存文件及 Redis 快照保持一致，先停止应用和 Redis。`COMPOSE_PATH` 必须改成当前部署实际使用的文件；一键部署使用 `./docker-compose.yml`：

```bash
cd /opt/ikik-api
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

本地目录版的默认数据库文件是 `data/ikik-api.db`。不要让多个 ikik-api 实例共享同一个 SQLite 文件。

### 2. 备份 MariaDB 部署

先停止应用写入，再执行逻辑备份，确保 SQL 快照与随后复制的 `/app/data`、Redis 数据使用同一业务截止点。数据库密码不会展开到宿主机进程参数；清理 trap 会在后续步骤失败时恢复服务。该方式同样兼容本地目录和命名卷：

```bash
cd /opt/ikik-api
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

# 先停止应用写入，使 SQL 快照与其余持久化数据保持同一业务截止点。
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

不要把运行中的 `mariadb_data/` 目录直接打包后当成一致性数据库备份。需要物理备份时，应使用 `mariadb-backup`，或停止 MariaDB 后制作存储快照。

不要执行：

```bash
docker compose -f "$COMPOSE_PATH" down -v
```

`down -v` 会删除命名卷。生产环境除非明确清空全部数据，否则禁止使用。

### 3. 修改镜像 tag

编辑服务器上的 `.env`：

```dotenv
IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:vX.Y.Z
```

### 4. 拉取并重建应用容器

把 `COMPOSE_PATH` 设置为当前部署实际使用的文件；一键部署使用 `./docker-compose.yml`，手动部署可能使用 `./docker-compose.local.yml` 或 `./docker-compose.local.mysql.yml`。一键更新脚本只管理“已选模板保存为 `./docker-compose.yml`”的目录；检测到其他 YAML Compose 文件、override、`COMPOSE_FILE`，或现有容器标签指向不同配置文件时会拒绝执行，手动 `-f` 部署必须继续使用原文件更新：

```bash
# 一键安装使用 ./docker-compose.yml；手动本地目录版 MariaDB 部署
# 应改为 ./docker-compose.local.mysql.yml。
COMPOSE_PATH=./docker-compose.yml
docker login ghcr.io
docker compose -f "$COMPOSE_PATH" pull ikik-api
docker compose -f "$COMPOSE_PATH" up -d ikik-api
```

### 5. 验证

```bash
docker compose -f "$COMPOSE_PATH" ps
docker compose -f "$COMPOSE_PATH" logs --tail=200 ikik-api
curl -fsS http://127.0.0.1:8080/health
```

后台页面验证：

- 管理员登录正常。
- API Key 调用正常。
- 账号列表、我的账号、收益管理页面能正常加载。
- 新版本迁移日志没有报错。

## 回滚

将 `.env` 中的 `IKIK_API_IMAGE` 改回旧版本标签，再使用当前数据库对应的 Compose 拉取并重建应用容器。若新版本已经执行不可逆数据库迁移，还必须恢复升级前的 SQLite 或 MariaDB 备份；不能只回滚镜像。

## 服务器直接源码构建

不推荐生产环境在服务器直接从源码构建。需要临时测试时：

SQLite：

```bash
cd deploy
docker compose -f docker-compose.dev.yml up -d --build
```

MariaDB：

```bash
cd deploy
cp .env.example .env
# 编辑 .env，设置非空的 MARIADB_ROOT_PASSWORD 和 MARIADB_PASSWORD。
chmod 600 .env
docker compose -f docker-compose.dev.mysql.yml up -d --build
```

## 常见问题

### 镜像配置不正确怎么办

如果 `.env` 中仍把 `IKIK_API_IMAGE` 设置为没有仓库限定的 `ikik-api:latest`，说明镜像配置不正确。改为 GHCR 上带版本标签的完整镜像引用：

```bash
IKIK_API_IMAGE=ghcr.io/ipanel/sliderapiv2:vX.Y.Z-custom.1
```

`docker-compose.local.yml` 中对应的镜像声明应保持为 `${IKIK_API_IMAGE:-ghcr.io/ipanel/sliderapiv2:latest}`，由 `.env` 覆盖正式部署版本。

然后重新拉取并启动：

```bash
docker compose -f docker-compose.local.yml pull ikik-api
docker compose -f docker-compose.local.yml up -d ikik-api
```

### 合并后如何确认二开差异还在

对比当前分支和官方 tag：

```bash
git diff --stat vX.Y.Z..HEAD
git diff --name-status vX.Y.Z..HEAD
```

重点确认这些二开模块仍存在：

```bash
backend/internal/handler/user_account_handler.go
backend/internal/service/account_credential_import.go
backend/internal/handler/admin/account_share_policy_handler.go
backend/internal/repository/account_share_policy_repo.go
frontend/src/views/user/AccountsView.vue
frontend/src/components/account/CredentialImportModal.vue
frontend/src/components/admin/revenue/SharePolicyPanel.vue
frontend/src/components/admin/revenue/ShareSettlementsPanel.vue
```

### 合并时发现官方大改了账号或计费模块

不要为了通过编译删除二开逻辑。先停止合并并记录：

```bash
git status --short
git diff --name-only --diff-filter=U
```

然后重点审查：

- 账号权限边界是否仍由后端字段控制。
- 调度池是否会暴露私有账号或未通过公共账号。
- `usage_log_id/request_id` 是否仍能保证分成幂等。
- 消费用户扣费、账号主入账、平台净收益的金额方向是否被官方改动影响。
- 凭证导入的禁止字段校验是否被覆盖。

确认清楚后再继续解决冲突。

### 更新后登录全部失效

检查 `.env` 中是否固定设置：

```bash
JWT_SECRET
TOTP_ENCRYPTION_KEY
```

这两个值不能每次启动随机变化。已有生产环境不要随意改。

## 发布前检查清单

合并阶段：

- `git status --short` 干净。
- `go test ./...` 通过。
- `pnpm run typecheck` 通过。
- 没有 Git 冲突标记。
- 二开功能重点文件仍存在。

镜像阶段：

- 镜像 tag 包含官方版本和二开构建号。
- 镜像已推送到 `ghcr.io/ipanel/sliderapiv2`。
- `.env` 中的 `IKIK_API_IMAGE` 使用 `ghcr.io/ipanel/sliderapiv2:<版本标签>`，不使用无仓库限定的 `ikik-api:latest`。

部署阶段：

- 升级前已备份部署目录和当前使用的 SQLite 或 MariaDB。
- 没有执行会删除持久卷的 `docker compose down -v`。
- `.env` 中生产密钥固定。
- 更新后 `/health` 正常。
- 管理后台、账号调用、收益管理核心页面验证正常。
