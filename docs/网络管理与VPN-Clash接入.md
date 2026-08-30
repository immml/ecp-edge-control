# 网络管理 + VPN / Clash 接入

控制面板「组网」页新增两个 Tab：**网络管理**（WiFi / 网卡 / 测速 / 虚拟 MAC）与
**VPN / Clash**（导出可达内网的 Clash 配置）。

## 一、网络管理（Agent 指令：net_get / net_set）

底层全部走节点系统自带的 NetworkManager（`nmcli`）与标准工具（`iw` / `ping`），
Debian / Ubuntu 通用、arm64 无额外依赖；需要改配置时提权（免密 sudo 直行，
否则返回人工 sudo 脚本）。

| 功能 | 指令 | action | 说明 |
|---|---|---|---|
| 扫描 WiFi | `net_get` | `wifi_scan` | 附近 SSID：信号 / 安全 / 信道 |
| 连接 WiFi | `net_set` | `wifi_connect` | 参数 ssid / password |
| 当前 WiFi | `net_get` | `wifi_status` | 已连接 SSID / 信号 / 信道 |
| 信道检测 | `net_get` | `channel` | 当前网卡信道 / 频率 |
| 以太网信息 | `net_get` | `ethernet` | 活跃连接 IP / 网关 / DNS / 配置方式 |
| 设备总览 | `net_get` | `devices` | 全部网卡与连接状态 |
| IP 总览 | `net_get` | `ip` | `ip -4 addr` 输出 |
| Ping 测试 | `net_get` | `ping` | 参数 host / count |
| **速度测试** | `net_get` | `speedtest` | 需 speedtest-go 二进制（见下） |
| DHCP / 手动 / PPPoE | `net_set` | `ip_mode` | mode=dhcp / manual（address/gateway/dns）/ pppoe（iface/username/password） |
| 以太网编辑 | `net_set` | `ethernet_edit` | 新建或修改连接（method / address / gateway / dns） |
| **虚拟 MAC** | `net_set` | `virtual_mac` | 自动生成虚拟 MAC + 同子网 IP（沿用当前掩码），建 WiFi 连接并启用 |

### 速度测试（speedtest-go）

轮子：开源 [showwin/speedtest-go](https://github.com/showwin/speedtest-go)
（纯 Go，`--json` 输出，无外部依赖）。二进制已随本项目构建：

```
agent/dist/speedtest-go-linux-arm64
agent/dist/speedtest-go-linux-amd64
```

部署：放到节点上 **agent 二进制同目录**（最优先探测）或 `/opt/ecp-agent/bin/`、
`/usr/local/bin/`。一键安装脚本亦可顺手放置（scp 后 `chmod +x`）。

### 虚拟 MAC 说明

- MAC 生成规则：`unicast + locally administered`（首字节 b0 = 0x02），不会伪装
  为厂商 MAC
- IP 生成：取当前网卡子网（如 192.168.1.0/24）、保持掩码不变、主机位随机避开
  网关/广播
- 网关/DNS 从当前活跃连接继承；建独立连接 profile（`ecp-vmac-xxxx`）并启用
- **合规**：仅用于自有/授权设备的多身份接入场景（白名单网络测试、接口仿真）；
  不用于绕过他人网络认证

## 二、VPN / Clash（访问这群内网设备）

拓扑（控制面/常在线节点为网关）：

```
Clash 客户端 ──vmess+ws──> vpn.immml.top:443（Cloudflare 边缘）
                              └─ Tunnel ──ws 回源──> 网关 127.0.0.1:8444（xray）
                                                        └─ freedom 出站（可及内网/tailnet）
```

### 部署网关（deploy/xray-vpn.sh，在网关节点 root/sudo 执行）

```bash
sudo bash xray-vpn.sh [--port 8444] [--path /ecp-vpn] [--uuid <uuid>]
```

- 下载官方 xray-core（GitHub release，国内受限时按提示手动放置）
- 落地 `VMess + WebSocket` 入站（仅监听 127.0.0.1，不对外开放），freedom 出站
- systemd 常驻 `ecp-xray`；脚本打印 UUID / path / 下一步

### 建 Cloudflare Tunnel

参考已有域名部署经验（edge.api.immml.top 同为 Cloudflare 资产）：

1. Cloudflare Zero Trust → Tunnels → 创建隧道（cloudflared 以 systemd 跑在网关）
2. Public Hostname：`vpn.<你的域名>` → Service `http://127.0.0.1:8444`
   （Cloudflare Tunnel 原生支持 WebSocket 回源，xray ws 正是用它）

### 导出 Clash 配置

控制面板 → 组网 → **VPN / Clash** Tab → 填「入口域名 / 端口 / UUID / WS 路径」→
「导出 Clash 配置」→ 下载 `.yaml`。

- 节点类型 `vmess` + `ws`（**全 Clash 版本兼容**；Clash 原版不支持 VLESS，故用 VMess）
- 规则：内网段 `192.168/16、10/8、172.16/12、100.64/10（tailnet）` 走 VPN，
  其余 DIRECT；可在「附加网段」补其他段
- Clash 客户端导入后，**分组选择 VPN** 即可访问这些内网设备

### 安全提示

- xray 仅回环监听，公网入口只经 Cloudflare Tunnel（TLS 由 CF 签发）
- UUID 即凭据，泄露可在网关删掉重建；大流量走 Tunnel 有 CF 免费额度限制
- 若网关即控制面所在 PC：出门在外时 PC 关机则 VPN 不可用——建议网关放常在线节点（Pi）

## 三、真机验证记录（2026-08-30，Orange Pi 3B）

- `net_get devices`：wlan0（CMCC-fpeh）/ tailscale0 / docker0 全列出 ✅
- `net_get wifi_scan`：扫描到 CMCC-fpeh（100% 信号，WPA1 WPA2，信道 6）✅
- `net_get ping`：223.5.5.5 3 包 0 丢包，rtt 14-21ms ✅
- `net_get speedtest`：香港节点 25.8ms / 下载 2.88 Mbps / 上传 1.85 Mbps ✅
- `POST /api/v1/vpn/clash-config`：yaml 生成正确 ✅
- 踩坑：speedtest-go 输出 JSON 是 `servers[]` 数组（非对象），字段 `dl_speed/ul_speed`
  （字节/秒）与 `latency`（ns）；已按真实输出修正解析

## 四、兼容与边界

- 无 NetworkManager 的系统（如精简容器、systemd-networkd 只读环境）wifi/ethernet
  指令不可用——届时返回对应错误
- 写类操作（连接 WiFi / 改 IP / 建连接）在非 root-agent 环境返回
  `NEEDS_PRIVILEGE` + 人工 sudo 脚本
- 测速会占用带宽与 CPU（saving-mode 已降耗），Pi 高负载时数值偏低属预期