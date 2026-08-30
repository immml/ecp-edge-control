#!/usr/bin/env bash
# ============================================================================
# ECP 一键安装：接入边缘节点（新机初始化）
#
#   1. Tailscale —— 安装并接入 tailnet（--tailscale-authkey 或交互登录）
#   2. Docker   —— 安装（已装跳过；官方源失败给出手动指引）
#   3. 1Panel   —— 安装（已装跳过；--no-1panel 跳过；装完打印安全入口）
#   4. ecp-agent —— 下载/复用二进制 → 配置 agent.yaml + 注册密钥 →
#                   systemd 常驻 → 接入控制面（节点列表可见、可下发指令）
#
# 用法示例（root 或 sudo）：
#   sudo bash bootstrap.sh \
#     --server 100.68.202.101:7443 \
#     --key <控制面 keygen 生成的 REGISTRATION_KEY> \
#     --tailscale-authkey <tskey-auth-xxxx> \
#     --agent-file ./ecp-agent-linux-arm64        # 或 --agent-url <https://...>
#
# 参数：
#   --server <ip:port>        控制面 gRPC 地址（必填，如 100.68.202.101:7443）
#   --key <key>               注册密钥（服务端 `server.exe keygen` 生成，必填）
#   --agent-url <url>         Agent 二进制下载地址（二选一，url 优先）
#   --agent-file <path>       本地已有 Agent 二进制路径（二选一）
#   --agent-sha256 <hex>      二进制 SHA256 校验（可选，给则校验）
#   --tailscale-authkey <key> Tailscale 接入 authkey（可选；缺则交互式登录）
#   --no-1panel               跳过 1Panel 安装
#   --relay-url <url>         紧急通道 Worker 地址（可选，如 wss://relay.example.com/agent）
#   --relay-token <token>     紧急通道节点 token（可选）
#   --feishu-webhook <url>    告警推送飞书自定义机器人 Webhook（可选）
#   --prefix <dir>            Agent 安装目录（默认 /opt/ecp-agent）
#   --cfg-dir <dir>           配置目录（默认 /etc/ecp）
#   --svc <name>              systemd 服务名（默认 ecp-agent；隔离测试用之）
#   --force                   已安装 ecp-agent 时强制重装
#   --dry-run                 只检测与打印计划，不执行安装
# ============================================================================
set -uo pipefail

# ---------- 日志 ----------
C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'; C_CYAN=$'\033[36m'; C_RESET=$'\033[0m'
info()  { echo "[${C_CYAN}INFO${C_RESET}] $*"; }
ok()    { echo "[${C_GREEN} OK ${C_RESET}] $*"; }
warn()  { echo "[${C_YELLOW}WARN${C_RESET}] $*"; }
err()   { echo "[${C_RED}ERR ${C_RESET}] $*" >&2; }
fail()  { err "$*"; exit 1; }

# ---------- 默认值 ----------
SERVER=""; REG_KEY=""; AGENT_URL=""; AGENT_FILE=""; AGENT_SHA256=""
TS_AUTHKEY=""; WITH_1PANEL=1; RELAY_URL=""; RELAY_TOKEN=""; FEISHU_WEBHOOK=""
FORCE=0; DRY_RUN=0
PREFIX=/opt/ecp-agent          # agent 数据目录（测试可改 /tmp/ecp-test）
CFG_DIR=/etc/ecp               # 配置目录（测试可改 /tmp/ecp-test-cfg）
SVC=ecp-agent

# ---------- 参数解析 ----------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)             SERVER=$2; shift 2 ;;
    --key)                REG_KEY=$2; shift 2 ;;
    --agent-url)          AGENT_URL=$2; shift 2 ;;
    --agent-file)         AGENT_FILE=$2; shift 2 ;;
    --agent-sha256)       AGENT_SHA256=$2; shift 2 ;;
    --tailscale-authkey)  TS_AUTHKEY=$2; shift 2 ;;
    --no-1panel)          WITH_1PANEL=0; shift ;;
    --relay-url)          RELAY_URL=$2; shift 2 ;;
    --relay-token)        RELAY_TOKEN=$2; shift 2 ;;
    --feishu-webhook)     FEISHU_WEBHOOK=$2; shift 2 ;;
    --prefix)             PREFIX=$2; shift 2 ;;
    --cfg-dir)            CFG_DIR=$2; shift 2 ;;
    --svc)                SVC=$2; shift 2 ;;
    --force)              FORCE=1; shift ;;
    --dry-run)            DRY_RUN=1; shift ;;
    -h|--help)
      sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) fail "未知参数: $1（-h 查看帮助）" ;;
  esac
done

# ---------- 前置检查 ----------
[[ "$(id -u)" == 0 ]] || fail "请以 root 运行：sudo bash bootstrap.sh ..."
command -v curl >/dev/null || fail "缺少 curl：apt-get install -y curl"
uname -m | grep -qE 'x86_64|amd64' && ARCH=amd64 || true
uname -m | grep -qE 'aarch64|arm64' && ARCH=arm64 || true
[[ -n "${ARCH:-}" ]] || fail "不支持的架构: $(uname -m)（支持 amd64 / arm64）"
OS_ID="$(. /etc/os-release; echo "${ID:-unknown}")"
info "系统: $OS_ID / $(uname -m) / 架构: $ARCH"

# ---------- 工具函数 ----------
cmd_exists() { command -v "$1" >/dev/null 2>&1; }
unit_active() { systemctl is-active --quiet "$1" 2>/dev/null; }

# 已安装判定 & 干跑
check_step() { # name, predicate_exec...
  local name=$1; shift
  if "$@" >/dev/null 2>&1; then ok "$name 已安装，跳过"; return 0; fi
  if [[ $DRY_RUN == 1 ]]; then info "计划安装 $name"; return 1; fi
  return 1
}

# ============================================================================
# 1) Tailscale：安装并接入
# ============================================================================
install_tailscale() {
  echo; echo "-------- [1/4] Tailscale 组网 --------"
  if cmd_exists tailscale && unit_active tailscaled; then
    ok "Tailscale 已安装且运行（$(tailscale status --json 2>/dev/null | grep -o '"Self":[^}]*"DNSName":"[^"]*"' | head -1)）"
  else
    if check_step "Tailscale" cmd_exists tailscale; then :; else
      info "安装 Tailscale..."
      if curl -fsSL https://tailscale.com/install.sh | sh; then
        ok "Tailscale 安装完成"
      else
        warn "官方源安装失败（国内网络可能受限）。备选："
        warn "  curl -fsSL https://pkgs.tailscale.com/stable/$(uname -m)/ | 手动下载 .deb 安装"
        warn "  或访问 https://tailscale.com/download 获取安装包"
        return 1
      fi
    fi
    systemctl enable --now tailscaled >/dev/null 2>&1 || true
    sleep 2
  fi

  if ! cmd_exists tailscale; then
    if [[ $DRY_RUN == 1 ]]; then warn "[dry-run] Tailscale 未安装，跳过接入检查"; return 0; fi
    fail "Tailscale 不可用，无法继续"
  fi
  if ! tailscale ip -4 >/dev/null 2>&1; then
    if [[ -n "$TS_AUTHKEY" ]]; then
      info "接入 tailnet（authkey）..."
      if [[ $DRY_RUN == 1 ]]; then info "[dry-run] tailscale up --authkey=<key>"; else
        tailscale up --authkey="$TS_AUTHKEY" --hostname="${HOSTNAME:-ecp-node}" || fail "Tailscale 接入失败，请检查 authkey 是否有效"
      fi
    else
      info "未提供 authkey，启动交互式登录（浏览器授权后自动完成）..."
      if [[ $DRY_RUN == 1 ]]; then info "[dry-run] tailscale up（交互登录）"; else
        tailscale up --hostname="${HOSTNAME:-ecp-node}" || fail "Tailscale 交互登录失败"
      fi
    fi
  fi
  ok "Tailscale 接入完成：$(tailscale ip -4 2>/dev/null | head -1)"
}

# ============================================================================
# 2) Docker
# ============================================================================
install_docker() {
  echo; echo "-------- [2/4] Docker --------"
  if cmd_exists docker && docker info >/dev/null 2>&1; then
    ok "Docker 已安装（$(docker --version)）"
  elif check_step "Docker" cmd_exists docker; then :; else
    info "安装 Docker（官方源）..."
    if curl -fsSL https://get.docker.com | sh; then
      systemctl enable --now docker >/dev/null 2>&1 || true
      ok "Docker 安装完成：$(docker --version)"
    else
      warn "官方源安装失败。可尝试阿里云镜像源："
      warn "  curl -fsSL https://get.docker.com | sh -s -- --mirror Aliyun"
      warn "  或参考 https://docs.docker.com/engine/install/debian/ 手动安装"
      return 1
    fi
  fi
}

# ============================================================================
# 3) 1Panel
# ============================================================================
install_1panel() {
  if [[ $WITH_1PANEL == 0 ]]; then
    echo; echo "-------- [3/4] 1Panel（已指定 --no-1panel，跳过）--------"
    return 0
  fi
  echo; echo "-------- [3/4] 1Panel --------"
  if cmd_exists 1pctl; then
    ok "1Panel 已安装（1pctl 存在）"
  elif check_step "1Panel" cmd_exists 1pctl; then :; else
    info "安装 1Panel（官方快速安装脚本）..."
    if [[ $DRY_RUN == 1 ]]; then info "[dry-run] 运行 1Panel quick_start.sh"; return 0; fi
    if echo | curl -fsSL https://resource.fit2cloud.com/1panel/package/quick_start.sh | sh; then
      ok "1Panel 安装完成"
      echo "  ※ 安全入口（不绕过其自身鉴权）："
      echo "    检查命令：1pctl user-info  或  cat /usr/local/1panel/1pctl user-info"
    else
      warn "1Panel 安装失败，可稍后手动执行："
      warn "  curl -sSL https://resource.fit2cloud.com/1panel/package/quick_start.sh -o quick_start.sh && sudo bash quick_start.sh"
      return 1
    fi
  fi
}

# ============================================================================
# 4) ecp-agent：安装并接入控制面
# ============================================================================
install_agent() {
  echo; echo "-------- [4/4] ECP Agent 接入 --------"
  [[ -n "$SERVER" ]] || fail "--server 必填（控制面 gRPC 地址，如 100.68.202.101:7443）"
  [[ -n "$REG_KEY" ]] || fail "--key 必填（控制面执行 server 的 keygen 生成）"

  # 4.1 幂等判断
  if [[ $FORCE == 0 ]] && cmd_exists "$PREFIX/ecp-agent" && unit_active "$SVC"; then
    ok "ecp-agent 已安装且运行中（--force 可重装）"
    verify_agent_connected || warn "agent 运行中但未确认接入状态"
    return 0
  fi
  if [[ $DRY_RUN == 1 ]]; then info "计划：安装 ecp-agent → $PREFIX（配置 $CFG_DIR，服务 $SVC）"; return 0; fi

  # 4.2 专用运行用户
  local RUN_USER=ecp-agent
  if ! id "$RUN_USER" >/dev/null 2>&1; then
    useradd --system --home "$PREFIX" --shell /usr/sbin/nologin "$RUN_USER" 2>/dev/null \
      || useradd --system --home "$PREFIX" "$RUN_USER"
    ok "创建运行用户 $RUN_USER"
  fi

  # 4.3 获取二进制
  local BIN="$PREFIX/ecp-agent"
  mkdir -p "$PREFIX" "$CFG_DIR"
  if [[ -n "$AGENT_FILE" ]]; then
    [[ -f "$AGENT_FILE" ]] || fail "agent 文件不存在: $AGENT_FILE"
    install -m 0755 "$AGENT_FILE" "$BIN"
    ok "使用本地二进制: $AGENT_FILE"
  elif [[ -n "$AGENT_URL" ]]; then
    info "下载 Agent 二进制: $AGENT_URL"
    curl -fsSL --retry 3 "$AGENT_URL" -o "$BIN.new" || fail "下载失败: $AGENT_URL"
    chmod 0755 "$BIN.new"; mv "$BIN.new" "$BIN"
    ok "下载完成"
  else
    fail "缺少 Agent 二进制来源：请用 --agent-file 指定本地文件，或 --agent-url 指定下载地址"
  fi
  if [[ -n "$AGENT_SHA256" ]]; then
    local got; got=$(sha256sum "$BIN" | awk '{print $1}')
    [[ "$got" == "$AGENT_SHA256" ]] || fail "SHA256 校验失败：期望 $AGENT_SHA256，实际 $got"
    ok "SHA256 校验通过"
  fi
  chown -R "$RUN_USER":"$RUN_USER" "$PREFIX"

  # 4.4 注册密钥（agent 以专用用户读写 /etc/ecp：首启会在此生成 client.key/client.crt，
  # 目录与文件必须归运行用户，否则身份加载 Permission denied）
  printf '%s\n' "$REG_KEY" > "$CFG_DIR/registration.key"
  chmod 0600 "$CFG_DIR/registration.key"

  # 4.5 告警规则（默认空规则，Agent 启动需要该文件存在）
  if [[ ! -f "$CFG_DIR/alert-rules.yaml" ]]; then
    cat > "$CFG_DIR/alert-rules.yaml" <<'YAML'
# ECP 节点本地告警规则（控制面板「告警」页可管理；模板见 agent.yaml 说明）
rules: []
YAML
  fi

  # 4.6 agent.yaml
  cat > "$CFG_DIR/agent.yaml" <<YAML
agent:
  node_id: ""           # 空则自动生成并持久化
  data_dir: $PREFIX
  config_dir: $CFG_DIR
  log_level: info

registration:
  key_file: $CFG_DIR/registration.key

control_plane:
  endpoints:
    - "$SERVER"         # 控制面 gRPC（Tailscale 直连）
  known_endpoints_file: $CFG_DIR/known_endpoints.json
  ca_cert: $CFG_DIR/ca.crt
  client_cert: $CFG_DIR/client.crt
  client_key: $CFG_DIR/client.key

relay:
  enabled: $( [[ -n "$RELAY_URL" ]] && echo true || echo false )
  url: "${RELAY_URL:-}"
  token: ""             # 走 systemd 环境变量 ECP_RELAY_TOKEN

telemetry:
  interval: 30s

cache:
  path: $PREFIX/cache.db
  retention: 168h

alert:
  rules_file: $CFG_DIR/alert-rules.yaml
  feishu_webhook: ""    # 走 systemd 环境变量 ECP_FEISHU_WEBHOOK

docker:
  managed_label: ecp.managed
YAML
  chmod 0644 "$CFG_DIR/agent.yaml"
  ok "生成配置 $CFG_DIR/agent.yaml"

  # 4.7 systemd 单元
  cat > /etc/systemd/system/$SVC.service <<YAML
[Unit]
Description=ECP Edge Agent (边缘节点控制平台 Agent)
After=network-online.target tailscaled.service docker.service
Wants=network-online.target

[Service]
Type=simple
User=$RUN_USER
WorkingDirectory=$PREFIX
$( [[ -n "$FEISHU_WEBHOOK" ]] && echo "Environment=ECP_FEISHU_WEBHOOK=$FEISHU_WEBHOOK" )
$( [[ -n "$RELAY_TOKEN" ]] && echo "Environment=ECP_RELAY_TOKEN=$RELAY_TOKEN" )
ExecStart=$BIN run -c $CFG_DIR/agent.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
YAML
  # 单元内不再落盘 token 明文：token 走 EnvironmentFile $CFG_DIR/relay.env（0600）
  if [[ -n "$RELAY_TOKEN" ]]; then
    cat > "$CFG_DIR/relay.env" <<YAML
ECP_RELAY_TOKEN=$RELAY_TOKEN
YAML
    chmod 0600 "$CFG_DIR/relay.env"
    sed -i '/^Environment=ECP_RELAY_TOKEN/d' /etc/systemd/system/$SVC.service
    sed -i '/^Environment=ECP_FEISHU/a EnvironmentFile='"$CFG_DIR"'/relay.env' /etc/systemd/system/$SVC.service
  fi
  # 配置目录整体归运行用户（agent 首启要生成 client.key/client.crt 等凭据）
  chown -R "$RUN_USER":"$RUN_USER" "$CFG_DIR"
  chmod 0700 "$CFG_DIR" 2>/dev/null || true
  systemctl daemon-reload
  systemctl enable --now $SVC >/dev/null 2>&1 || true
  ok "服务已启动（systemctl status $SVC）"

  # 4.8 接入验证
  sleep 6
  verify_agent_connected
}

verify_agent_connected() {
  info "验证接入状态（最近日志）..."
  journalctl -u "$SVC" --no-pager -n 12 2>/dev/null | grep -aE "已连接|接入|注册|控制面|relay|紧急通道" | tail -4 \
    || journalctl -u "$SVC" --no-pager -n 12 2>/dev/null | tail -4
  if unit_active "$SVC"; then
    ok "ecp-agent 运行中；请到控制面板「节点列表」确认该节点在线"
  else
    warn "ecp-agent 未处于运行状态，请查看：journalctl -u $SVC -n 50"
  fi
}

# ============================================================================
# 汇总
# ============================================================================
summary() {
  echo; echo "============================================================"
  echo " ECP 节点初始化完成"
  echo "============================================================"
  echo " 系统       : $OS_ID / $(uname -m)"
  echo " Tailscale  : $(tailscale ip -4 2>/dev/null | head -1)（tailnet 内）"
  [[ $WITH_1PANEL == 1 ]] && cmd_exists 1pctl && echo " 1Panel     : 已安装（1pctl user-info 查看入口）"
  cmd_exists docker && echo " Docker     : $(docker --version 2>/dev/null)"
  echo " Agent      : $PREFIX/ecp-agent（服务 $SVC）"
  echo " 配置       : $CFG_DIR/agent.yaml"
  echo " ------------------------------------------------------------"
  echo " 下一步："
  echo "   1) 控制面板「节点列表」确认新节点上线"
  echo "   2) 节点详情 → 下发指令（如 docker ps）验证链路"
  echo "   3) 组网页配置 FRP / 紧急通道可选"
  echo " 卸载：  systemctl disable --now $SVC && rm -rf $PREFIX $CFG_DIR /etc/systemd/system/$SVC.service"
  echo "============================================================"
}

# ============================================================================
main() {
  info "ECP 一键安装开始（dry-run=$DRY_RUN）"
  install_tailscale
  install_docker
  install_1panel
  install_agent
  summary
}

main "$@"