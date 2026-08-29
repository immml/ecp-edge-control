#!/usr/bin/env python3
"""探测边缘节点真实环境。

目的：一次性拿到架构待确认项的事实答案（Tailscale 现状、1Panel 端口与入口、
Docker 可见性、监听端口占用、系统版本），供架构与开发决策使用。

用法（Linux / Git Bash）：
    .venv/tools/Scripts/python.exe scripts/probe_node.py

凭据从 .deploy/ssh/credentials.json 读取（该目录已被 .gitignore 排除）。
sudo 密码通过 stdin 写入，不出现在命令行里，因此不会进入 shell history。
"""

import json
import sys
from datetime import datetime
from pathlib import Path

import paramiko

ROOT = Path(__file__).resolve().parent.parent
CRED_FILE = ROOT / ".deploy" / "ssh" / "credentials.json"

# (标题, 命令)
NORMAL_COMMANDS = [
    ("系统标识", "uname -a"),
    ("发行版", "head -4 /etc/os-release"),
    ("主机名", "hostname"),
    ("运行时长与负载", "uptime"),
    ("当前用户与组", "id"),
    ("CPU 架构", "dpkg --print-architecture 2>/dev/null || uname -m"),
    ("内存", "free -h | head -2"),
    ("根分区磁盘", "df -h / | tail -1"),
    ("CPU 温度(毫摄氏度)", "cat /sys/class/thermal/thermal_zone0/temp 2>/dev/null || echo 'no thermal zone'"),
    ("SECTION:Tailscale", "echo ''"),
    ("tailscale 是否安装", "command -v tailscale || echo 'NOT INSTALLED'"),
    ("tailscale 版本", "tailscale version 2>/dev/null || echo '-'"),
    ("tailscale IPv4", "tailscale ip -4 2>/dev/null || echo 'NOT AVAILABLE'"),
    ("tailscale 状态", "tailscale status 2>&1 | head -15 || echo '-'"),
    ("SECTION:网络", "echo ''"),
    ("网卡与地址", "ip -o addr show | awk '{print $2, $4}'"),
    ("默认路由", "ip route | head -3"),
    ("SECTION:Docker", "echo ''"),
    ("docker 版本", "docker --version 2>&1 || echo 'docker 不可见'"),
    ("运行中容器数", "docker ps -q 2>/dev/null | wc -l"),
    ("SECTION:1Panel", "echo ''"),
    ("1pctl 位置", "command -v 1pctl || echo 'NOT IN PATH'"),
    ("1Panel 安装目录", "ls -d /opt/1panel 2>/dev/null || echo 'no /opt/1panel'"),
]

# 需要提权的命令
SUDO_COMMANDS = [
    ("[sudo] 1Panel 面板信息", "1pctl user-info 2>&1"),
    ("[sudo] 1Panel 运行状态", "1pctl status 2>&1 | head -8"),
    ("[sudo] TCP 监听端口与进程", "ss -tlnp 2>/dev/null | head -30"),
    ("[sudo] sudo 免密检查", "sudo -n true 2>&1 && echo 'SUDO_NOPASSWD_OK' || echo 'sudo 需要密码'"),
]


def run(client, title, cmd, timeout=20):
    try:
        _, stdout, stderr = client.exec_command(cmd, timeout=timeout)
        out = stdout.read().decode("utf-8", errors="replace").strip()
        err = stderr.read().decode("utf-8", errors="replace").strip()
        return out or err or "(空)"
    except Exception as exc:  # noqa: BLE001
        return f"ERROR: {exc}"


def run_sudo(client, cmd, password, timeout=30):
    """通过 stdin 传密码，避免密码出现在命令行（不进 history / ps）。"""
    try:
        stdin, stdout, _ = client.exec_command(f"sudo -S -p '' {cmd}", get_pty=True, timeout=timeout)
        stdin.write(password + "\n")
        stdin.flush()
        raw = stdout.read().decode("utf-8", errors="replace")
        lines = [
            line
            for line in raw.splitlines()
            if not line.strip().startswith("[sudo]") and line.strip() != password
        ]
        return "\n".join(lines).strip() or "(空)"
    except Exception as exc:  # noqa: BLE001
        return f"ERROR: {exc}"


def main() -> int:
    if not CRED_FILE.exists():
        print(f"凭据文件不存在: {CRED_FILE}", file=sys.stderr)
        return 1

    cred = json.loads(CRED_FILE.read_text(encoding="utf-8"))
    host = cred["host"]
    user = cred["username"]
    password = cred.get("password", "")

    print("# 边缘节点环境探测报告\n")
    print(f"- 目标：**{user}@{host}:{cred.get('port', 22)}**")
    print(f"- 探测时间：{datetime.now().isoformat(timespec='seconds')}\n")

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())

    try:
        client.connect(
            host,
            port=cred.get("port", 22),
            username=user,
            password=password,
            timeout=20,
            allow_agent=False,
            look_for_keys=False,
        )
    except Exception as exc:  # noqa: BLE001
        print(f"## 连接失败\n\n```\n{exc}\n```")
        return 1

    print("## 一、普通权限探测\n")
    for title, cmd in NORMAL_COMMANDS:
        if title.startswith("SECTION:"):
            print(f"\n## {title.split(':', 1)[1]}\n")
            continue
        print(f"**{title}**\n```\n{run(client, title, cmd)}\n```\n")

    print("## 二、提权探测\n")
    for title, cmd in SUDO_COMMANDS:
        print(f"**{title}**\n```\n{run_sudo(client, cmd, password)}\n```\n")

    client.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
