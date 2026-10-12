#!/usr/bin/env bash
# ============================================================================
# 慧沐引擎 · 一键安装部署脚本（高可用 · Docker Compose）
#
# 用法（Ubuntu/Debian 服务器，root 执行）：
#   bash deploy/install.sh                 # 在已克隆的仓库内执行
#   TG_ADMIN_PASSWORD='你的密码' bash deploy/install.sh   # 预置 admin 密码
#
# 脚本做六件事（对应部署文档 1.2 步骤 0-3）：
#   1. 检查/安装基础工具与 Docker
#   2. 克隆私有仓库（需先配好 SSH Deploy Key，见文档步骤 1）
#   3. 生成 deploy/.env（密码与密钥全部随机，自动 chmod 600）
#   4. 构建镜像并启动 postgres/redis/双网关/caddy 五容器
#   5. 轮询健康检查直至就绪
#   6. 打印访问地址、管理员账号与后续动作
#
# 幂等：重复执行安全——.env 已存在则复用，容器已存在则原位更新。
# ============================================================================
set -euo pipefail

# ---------- 输出工具 ----------
G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; B=$'\033[1m'; N=$'\033[0m'
STEP=0; TOTAL=6
step() { STEP=$((STEP+1)); printf '\n%b%s步骤 %d/%d · %s%b\n' "${G}${B}" "=========" "$STEP" "$TOTAL" "$*" "$N"; }
ok()   { printf '%b[✓]%b %s\n' "$G" "$N" "$*"; }
info() { printf '%b[i]%b %s\n' "$B" "$N" "$*"; }
warn() { printf '%b[!]%b %s\n' "$Y" "$N" "$*"; }
die()  { printf '%b[✗]%b %s\n' "$R" "$N" "$*"; exit 1; }

# ---------- 常量 ----------
REPO_SSH="git@github.com:lx-ruc/general-api.git"
INSTALL_DIR="/opt/huimu-engine"          # 源码与 compose 的落位目录
GO_MIN=1.22                              # 可选本机构建依赖的最低版本（容器构建无需）

ver_ge() { [ "$(printf '%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]; }
rand_pw() { openssl rand -base64 18 | tr -d '/+=' | cut -c1-20; }

PKGINST=""
if command -v apt-get >/dev/null 2>&1; then PKGINST="apt"
elif command -v yum >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1; then PKGINST="yum"
else die "不支持的发行版：请手动安装 git 与 Docker 后重试"; fi

[ "$(id -u)" -eq 0 ] || die "请用 root 执行（sudo bash deploy/install.sh）"

# ============================================================================
step "基础工具（git / curl / openssl / sqlite3）"
# ============================================================================
need=()
command -v git     >/dev/null 2>&1 || need+=(git)
command -v curl    >/dev/null 2>&1 || need+=(curl)
command -v openssl >/dev/null 2>&1 || need+=(openssl)
command -v sqlite3 >/dev/null 2>&1 || need+=(sqlite3)
if [ "${#need[@]}" -gt 0 ]; then
    info "安装缺失工具：${need[*]}"
    if [ "$PKGINST" = "apt" ]; then
        apt-get update -qq && apt-get install -y -qq "${need[@]}"
    else
        (yum install -y -q "${need[@]}" 2>/dev/null || dnf install -y -q "${need[@]}")
    fi
fi
ok "git $(git --version | awk '{print $3}') / curl / openssl / sqlite3 就绪"

# ============================================================================
step "Docker Engine 与 Compose 插件"
# ============================================================================
if ! command -v docker >/dev/null 2>&1; then
    info "安装 Docker（发行版源）"
    if [ "$PKGINST" = "apt" ]; then
        apt-get install -y -qq docker.io docker-compose-v2 \
            || die "Docker 安装失败：可改用 Docker 官方安装源后重跑本脚本"
    else
        (yum install -y -q docker 2>/dev/null || dnf install -y -q docker) \
            || die "请先按 Docker 官方文档安装 Engine 与 compose 插件后重跑本脚本"
    fi
fi
systemctl enable --now docker >/dev/null 2>&1 || true
docker info >/dev/null 2>&1 || die "Docker 未运行：systemctl status docker 排查"
docker compose version >/dev/null 2>&1 || die "缺少 compose 插件：apt-get install -y docker-compose-v2"
ok "$(docker --version)｜$(docker compose version --short)"

# 可选本机构建依赖（容器化部署用不到，仅提示，不阻塞）
command -v go >/dev/null 2>&1 \
    && { ver_ge "$(go version | awk '{print $3}' | sed 's/go//')" "$GO_MIN" \
         && ok "本机 go ≥ $GO_MIN（可选依赖，已满足）" \
         || warn "本机 go < $GO_MIN（仅影响脱离容器的本机构建，容器部署可忽略）"; } \
    || info "本机未装 go（容器构建无需，可忽略）"

# ============================================================================
step "获取源码（私有仓库 · SSH）"
# ============================================================================
if [ -f "$(dirname "$0")/docker-compose.yml" ]; then
    # 脚本从仓库内执行：源码就在原地
    DEPLOY_DIR="$(cd "$(dirname "$0")" && pwd)"
    info "在仓库内执行，部署目录：$DEPLOY_DIR"
elif [ -f "$INSTALL_DIR/deploy/docker-compose.yml" ]; then
    DEPLOY_DIR="$INSTALL_DIR/deploy"
    info "已存在 $INSTALL_DIR，复用（更新代码请自行 git pull）"
else
    info "克隆 $REPO_SSH → $INSTALL_DIR"
    GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=accept-new" \
        git clone -q "$REPO_SSH" "$INSTALL_DIR" \
        || die "克隆失败：私有仓库需先配置 SSH Deploy Key（部署文档 1.2 步骤 1）"
    DEPLOY_DIR="$INSTALL_DIR/deploy"
fi
cd "$DEPLOY_DIR"
ok "源码就绪：$(git -C "$DEPLOY_DIR/.." log --oneline -1 2>/dev/null | head -1 || echo 已克隆)"

# ============================================================================
step "生成 deploy/.env（密钥自动随机）"
# ============================================================================
if [ -f .env ]; then
    warn ".env 已存在，复用现有配置（如需重置请先备份删除）"
else
    PG_PASSWORD="${PG_PASSWORD:-$(rand_pw)}"
    TG_JWT_SECRET="${TG_JWT_SECRET:-$(openssl rand -hex 32)}"
    TG_ADMIN_PASSWORD="${TG_ADMIN_PASSWORD:-}"
    if [ -z "$TG_ADMIN_PASSWORD" ] && [ -t 0 ]; then
        read -r -p "设置 admin 初始密码（回车 = 自动生成）: " TG_ADMIN_PASSWORD
    fi
    TG_ADMIN_PASSWORD="${TG_ADMIN_PASSWORD:-$(rand_pw)}"

    cat > .env <<EOF
# 慧沐引擎 deploy/.env —— 由 install.sh 生成于 $(date '+%F %T')
# 含密钥，严禁提交仓库或外泄；修改后需 docker compose up -d 重建生效

# PostgreSQL 密码（数据库与网关 DSN 共用）
PG_PASSWORD=${PG_PASSWORD}

# JWT 签名密钥
TG_JWT_SECRET=${TG_JWT_SECRET}

# 首次启动创建的 admin 初始密码（仅 users 表为空时生效一次）
TG_ADMIN_PASSWORD=${TG_ADMIN_PASSWORD}

# 镜像内 Go 模块拉取代理（海外服务器可删掉此行走官方源）
TG_BUILD_GOPROXY=https://goproxy.cn,direct
EOF
    chmod 600 .env
    ok ".env 已生成（权限 600）"
fi

# 端口占用预检（80/443 归 caddy；若被本集群之外的进程占用则终止）
for p in 80 443; do
    if command -v ss >/dev/null 2>&1 && ss -ltn "( sport = :$p )" | grep -q LISTEN; then
        docker ps --format '{{.Names}} {{.Ports}}' 2>/dev/null | grep -q ":$p->" \
            && continue   # 本集群 caddy 自身占用，属正常
        warn "端口 $p 已被其他进程占用：$(ss -ltnp "sport = :$p" | tail -1)"
        die "请先停掉占用 80/443 的服务（如 nginx/apache），或换机部署"
    fi
done
ok "端口 80/443 可用"

# ============================================================================
step "构建镜像并启动五容器（首次构建需拉取依赖，约 3-10 分钟）"
# ============================================================================
docker compose up -d --build
ok "docker compose 已拉起"

# ============================================================================
step "健康检查与部署摘要"
# ============================================================================
info "等待网关就绪（最长 5 分钟）"
ready=0
for i in $(seq 1 60); do
    if curl -sf -m 3 "http://127.0.0.1/healthz" >/dev/null 2>&1; then ready=1; break; fi
    sleep 5
done
[ "$ready" -eq 1 ] || {
    warn "healthz 暂未就绪，最近日志："
    docker compose logs --tail=30 gateway1 || true
    die "启动异常：docker compose logs gateway1 排查后重跑本脚本"
}
ok "healthz 返回 200，五容器全部就绪"

ADMIN_PW="$(grep '^TG_ADMIN_PASSWORD=' .env | cut -d= -f2-)"
SERVER_IP="$(curl -sf -m 3 https://ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')"
if grep -q '^:80' Caddyfile 2>/dev/null; then DOMAIN_HINT="（当前 Caddyfile 为 :80，仅 HTTP；绑定域名：把 :80 改成你的域名后执行 docker compose restart caddy）"; else DOMAIN_HINT=""; fi

cat <<EOF

${G}${B}================ 部署成功 ================${N}
  管理台地址 : http://${SERVER_IP}/${DOMAIN_HINT}
  管理员账号 : admin
  初始密码   : ${ADMIN_PW}   （也保存在 deploy/.env）
  配置文件   : ${DEPLOY_DIR}/.env
  数据卷     : pgdata（数据库）、caddydata（证书）

  常用命令（在 ${DEPLOY_DIR} 下执行）：
    docker compose ps                 # 查看五容器状态
    docker compose logs -f gateway1   # 跟踪网关日志
    docker compose up -d --build      # 改配置/升级后重建
    bash backup.sh                     # 手动备份一次（每日定时见部署文档 1.6）

  数据面接入：Base URL = http://${SERVER_IP}/v1 ，鉴权用子账号密钥 sk-...
${G}${B}==========================================${N}
EOF
