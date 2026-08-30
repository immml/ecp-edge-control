#!/usr/bin/env bash
# ============================================================================
# ECP VPN 网关：在网关节点（控制面/常在线节点）部署 xray-core，对外提供
# VMess+WebSocket 入站；经 Cloudflare Tunnel 暴露为 https://vpn.你的域名:443，
# Clash 客户端加载导出的配置即可访问这群内网设备。
#
# 拓扑：
#   Clash 客户端 ──wss vmess──> vpn.immml.top:443（Cloudflare 边缘）
#        └── Tunnel ──ws 回源──> 本机 127.0.0.1:8444（xray vmess+ws）
#                                  └── freedom 出站（可访问内网/tailnet）
#
# 用法（root/sudo，在网关节点执行）：
#   sudo bash xray-vpn.sh [--port 8444] [--path /ecp-vpn] [--uuid <uuid>]
#
# 之后：
#   1) Cloudflare Zero Trust → Tunnels → 建隧道，Public Hostname:
#      vpn.你的域名 → Service http://127.0.0.1:8444（Tunnel 原生支持 ws 回源）
#   2) 控制面板「组网 → VPN/Clash」填 域名/uuid/端口/path → 导出 clash yaml
# ============================================================================
set -uo pipefail

C_GREEN=$'\033[32m'; C_YELLOW=$'\033[33m'; C_RED=$'\033[31m'; C_CYAN=$'\033[36m'; C_RESET=$'\033[0m'
info() { echo "[${C_CYAN}INFO${C_RESET}] $*"; }
ok()   { echo "[${C_GREEN} OK ${C_RESET}] $*"; }
warn() { echo "[${C_YELLOW}WARN${C_RESET}] $*"; }
fail() { echo "[${C_RED}ERR ${C_RESET}] $*" >&2; exit 1; }

PORT=""; PATH_WS=""; UUID=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)  PORT=$2; shift 2 ;;
    --path)  PATH_WS=$2; shift 2 ;;
    --uuid)  UUID=$2; shift 2 ;;
    -h|--help) sed -n '5,16p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) fail "未知参数: $1" ;;
  esac
done

[[ "$(id -u)" == 0 ]] || fail "请以 root 运行"
uname -m | grep -qE 'x86_64|amd64' && ARCH=amd64 || true
uname -m | grep -qE 'aarch64|arm64' && ARCH=arm64 || true
[[ -n "${ARCH:-}" ]] || fail "不支持的架构: $(uname -m)"

PORT=${PORT:-8444}
PATH_WS=${PATH_WS:-/ecp-vpn}
UUID=${UUID:-$(cat /proc/sys/kernel/random/uuid)}
INSTALL_DIR=/opt/ecp-xray
XRAY=$INSTALL_DIR/xray

info "架构: $ARCH | 端口: 127.0.0.1:$PORT | path: $PATH_WS"

# 1) xray 二进制
if [[ ! -x "$XRAY" ]]; then
  info "下载 xray-core (linux/$ARCH)..."
  VER=$(curl -fsSL --max-time 20 https://api.github.com/repos/XTLS/Xray-core/releases/latest | grep -oE '"tag_name": *"[^"]+"' | head -1 | cut -d'"' -f4)
  [[ -n "$VER" ]] || VER="v25.1.30"
  URL="https://github.com/XTLS/Xray-core/releases/download/${VER}/Xray-linux-${ARCH}.zip"
  TMP=$(mktemp -d)
  if ! curl -fsSL --max-time 120 "$URL" -o "$TMP/x.zip"; then
    warn "GitHub 下载失败（国内受限），请手动下载：$URL"
    warn "解压出 xray 放到 $INSTALL_DIR/xray 后重跑本脚本"
    rm -rf "$TMP"
    exit 1
  fi
  mkdir -p "$INSTALL_DIR"
  (cd "$TMP" && unzip -oq x.zip && cp xray "$INSTALL_DIR/xray" 2>/dev/null || cp Xray "$INSTALL_DIR/xray" 2>/dev/null)
  chmod +x "$INSTALL_DIR/xray"
  rm -rf "$TMP"
  ok "xray 安装完成: $XRAY ($("$XRAY" version | head -1 2>/dev/null | awk '{print $2}'))"
fi

# 2) 配置（vmess + ws，仅回环监听；Tunnel 回源）
mkdir -p /etc/ecp-xray
CONF=/etc/ecp-xray/config.json
cat > "$CONF" <<JSON
{
  "inbounds": [{
    "listen": "127.0.0.1",
    "port": $PORT,
    "protocol": "vmess",
    "settings": { "clients": [{ "id": "$UUID", "alterId": 0 }] },
    "streamSettings": {
      "network": "ws",
      "wsSettings": { "path": "$PATH_WS" }
    },
    "sniffing": { "enabled": true, "destOverride": ["http", "tls"] }
  }],
  "outbounds": [
    { "protocol": "freedom", "tag": "direct" },
    { "protocol": "blackhole", "tag": "block" }
  ],
  "routing": {
    "rules": [
      { "type": "field", "protocol": ["bittorrent"], "outboundTag": "block" },
      { "type": "field", "ip": ["geoip:private"], "outboundTag": "direct" }
    ]
  }
}
JSON
chmod 0644 "$CONF"
ok "配置就绪 /etc/ecp-xray/config.json"

# 3) systemd
cat > /etc/systemd/system/ecp-xray.service <<UNIT
[Unit]
Description=ECP VPN Gateway (xray vmess+ws)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$XRAY run -c /etc/ecp-xray/config.json
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now ecp-xray >/dev/null 2>&1 || true
sleep 1
if systemctl is-active --quiet ecp-xray; then
  ok "xray 运行中（127.0.0.1:$PORT$PATH_WS）"
else
  warn "xray 未启动，请查看：journalctl -u ecp-xray -n 30"
fi

echo
echo "============================================================"
echo " VPN 网关就绪"
echo "============================================================"
echo " UUID        : $UUID"
echo " 入站        : vmess://ws 127.0.0.1:$PORT$PATH_WS（仅回环，勿对外）"
echo " Cloudflare  : Zero Trust → Tunnels → 建隧道，Public Hostname"
echo "               vpn.<你的域名> → Service: http://127.0.0.1:$PORT"
echo " Clash 配置  : 控制面板「组网 → VPN/Clash」导出"
echo " 卸载        : systemctl disable --now ecp-xray && rm -rf /opt/ecp-xray /etc/ecp-xray"
echo "============================================================"