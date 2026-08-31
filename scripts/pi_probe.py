#!/usr/bin/env python3
"""探测 Orange Pi 网络环境（WiFi 管理功能前置调研）。

- 连接：优先 Tailscale IP (100.108.234.5)，失败回退 LAN IP (192.168.1.5)
- 认证：用户 orangepi / 密码（环境变量 ECP_PI_PASS 或 argv[1]）
- 动作：安装本机公钥 (~/.ssh/ecp_pi.pub) → 采集网络环境信息 → 输出 JSON

只读探测 + 装公钥，不做任何破坏性操作。wpa_supplicant 密码等敏感内容不输出。
"""
import json
import os
import sys
import paramiko

HOSTS = ["100.108.234.5", "192.168.1.5"]
USER = "orangepi"
PASS = os.environ.get("ECP_PI_PASS") or (sys.argv[1] if len(sys.argv) > 1 else "orangepi")
PUBKEY_PATH = os.path.expanduser("~/.ssh/ecp_pi.pub")

# (名称, 命令) —— 全部只读
PROBES = [
    ("arch_os", "uname -m; . /etc/os-release && echo \"$PRETTY_NAME\""),
    ("tools", "for t in iw iwlist nmcli wpa_cli mihomo clash xray python3; do printf '%s: ' $t; command -v $t || echo MISSING; done; echo '---'; iw --version 2>/dev/null | head -1; nmcli --version 2>/dev/null | head -1"),
    ("net_mgrs", "systemctl list-unit-files --no-legend 2>/dev/null | grep -iE 'network-manager|wpa_supplicant|networkd|networking|tailscale' ; echo '---active---'; systemctl is-active NetworkManager 2>&1; systemctl is-active wpa_supplicant 2>&1"),
    ("nm_devices", "nmcli -t -f DEVICE,TYPE,STATE,CONNECTION device status 2>&1"),
    ("nm_active", "nmcli -t -f NAME,DEVICE,TYPE,SSID,SIGNAL,CHAN connection show --active 2>&1"),
    ("iw_link", "iw dev wlan0 link 2>&1 | head -20"),
    ("iw_info", "iw dev wlan0 info 2>&1 | head -15"),
    ("phy_bands", "iw phy 2>/dev/null | grep -E 'Band [0-9]|HT |VHT |HE |channel list|max AMPDU' | head -25"),
    ("ifaces", "ip -br -4 addr show 2>&1"),
    ("routes", "ip route show default 2>&1"),
    ("tailscale", "tailscale status 2>&1 | head -8"),
    ("wpa_conf", "sudo -n head -5 /etc/wpa_supplicant/wpa_supplicant.conf 2>/dev/null; echo '---'; sudo -n grep -cE '^\\s*network=' /etc/wpa_supplicant/wpa_supplicant.conf 2>/dev/null || echo 'parse-skip'"),
    ("nmd_conf", "ls /etc/NetworkManager/system-connections/ 2>/dev/null | head -20"),
    ("ecp_dirs", "ls -la /opt/ecp-agent/ 2>/dev/null | head -15; echo '---xray---'; ls /etc/ecp-xray/ 2>/dev/null; systemctl list-units --no-legend 2>/dev/null | grep -iE 'ecp|xray|clash|mihomo' | head -10"),
    ("resources", "df -h / | tail -1; free -h | head -2"),
    ("uptime", "uptime"),
    ("sudo_ok", "sudo -n true 2>&1 && echo SUDO_NOPASS_OK || echo SUDO_NEEDS_PASS"),
    ("clash_proc", "ps -eo pid,args 2>/dev/null | grep -iE '[c]lash|[m]ihomo|[x]ray|[f]rpc' | head -10; echo END"),
]


def shq(s: str) -> str:
    """bash 单引号转义，安全嵌入远程命令。"""
    return "'" + s.replace("'", "'\\''") + "'"


def _read_all(chan) -> str:
    """带超时读取 channel 全部输出（paramiko recv 循环）。"""
    chunks = []
    while True:
        try:
            data = chan.recv(65536)
        except Exception:  # noqa: BLE001  timeout / closed
            break
        if not data:
            break
        chunks.append(data)
    return b"".join(chunks).decode(errors="replace")


def main() -> int:
    if not os.path.exists(PUBKEY_PATH):
        print("ERR: 未找到公钥", PUBKEY_PATH)
        return 1
    pubkey = open(PUBKEY_PATH).read().strip()

    # 所有探测合成一个 bash 脚本，单 channel 执行（规避 sshd MaxSessions 限制）
    script_lines = ["set +e", 'echo "###KEY###"',
                    f"mkdir -p ~/.ssh && chmod 700 ~/.ssh && grep -qF {shq(pubkey)} ~/.ssh/authorized_keys 2>/dev/null || echo {shq(pubkey)} >> ~/.ssh/authorized_keys; chmod 600 ~/.ssh/authorized_keys && echo PUBKEY_OK"]
    for name, cmd in PROBES:
        script_lines.append(f'echo "###{name}###"')
        script_lines.append(f"timeout 30 bash -c {shq(cmd)} 2>&1")
    script_lines.append('echo "###END###"')
    full_script = "\n".join(script_lines)

    last_err = None
    for host in HOSTS:
        print(f"[i] 连接 {host} ...", file=sys.stderr)
        try:
            cli = paramiko.SSHClient()
            cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
            # 优先私钥（已验证过），回退密码
            kw = dict(username=USER, timeout=15)
            if os.path.exists(os.path.expanduser("~/.ssh/ecp_pi")):
                kw.update(key_filename=os.path.expanduser("~/.ssh/ecp_pi"),
                          look_for_keys=False, allow_agent=False)
            else:
                kw.update(password=PASS, look_for_keys=False, allow_agent=False)
            cli.connect(host, **kw)
            print(f"[i] {host} 连接成功", file=sys.stderr)

            stdin, out, err = cli.exec_command("timeout 300 bash -s", timeout=320)
            # 脚本经 stdin 喂给 bash -s（不依赖远程 shell 解析 heredoc）
            stdin.write(full_script + "\n")
            stdin.channel.shutdown_write()
            out.channel.settimeout(300)
            raw = _read_all(out.channel)
            raw += _read_all(err.channel)

            # 按 ###xxx### 分段解析
            result = {"host": host, "user": USER, "ssh_ok": True}
            cur = None
            for line in raw.splitlines():
                line = line.strip()
                if line.startswith("###") and line.endswith("###"):
                    cur = line.strip("#").strip()
                    result[cur] = ""
                    continue
                if cur and cur != "KEY":
                    result[cur] = (result.get(cur, "") + line + "\n").strip()
            if "KEY" in result:
                print("[i] 公钥:", result.pop("KEY"), file=sys.stderr)

            print(json.dumps(result, ensure_ascii=False, indent=1))
            cli.close()
            return 0
        except Exception as e:  # noqa: BLE001
            last_err = e
            print(f"[!] {host} 失败: {e}", file=sys.stderr)
            continue

    print("ERR: 全部主机连接失败:", last_err, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())