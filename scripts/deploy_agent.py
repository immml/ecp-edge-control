#!/usr/bin/env python3
"""把 Agent 二进制推送到边缘节点并执行验证命令。

用法（Linux / Git Bash）：
    .venv/tools/Scripts/python.exe scripts/deploy_agent.py upload
    .venv/tools/Scripts/python.exe scripts/deploy_agent.py caps

设计说明：只上传到用户家目录，不写系统目录，
因此不需要提权——这样即使节点上没有 sudo 也能验证。
"""

import json
import stat
import sys
from pathlib import Path

import paramiko

ROOT = Path(__file__).resolve().parent.parent
CRED_FILE = ROOT / ".deploy" / "ssh" / "credentials.json"
LOCAL_BIN = ROOT / ".venv" / "agent" / "bin" / "ecp-agent-linux-arm64"
# SFTP 协议不展开 ~，必须用绝对路径；家目录则通过远端 whoami 解析
REMOTE_DIR = "/home/orangepi"
REMOTE_BIN = REMOTE_DIR + "/ecp-agent-test"


def connect():
    cred = json.loads(CRED_FILE.read_text(encoding="utf-8"))
    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    client.connect(
        cred["host"],
        port=cred.get("port", 22),
        username=cred["username"],
        password=cred["password"],
        timeout=20,
        allow_agent=False,
        look_for_keys=False,
    )
    return client, cred


def upload() -> int:
    if not LOCAL_BIN.exists():
        print(f"本地二进制不存在，先执行交叉编译: {LOCAL_BIN}")
        return 1

    client, _ = connect()
    try:
        sftp = client.open_sftp()
        print(f"上传 {LOCAL_BIN.name} ({LOCAL_BIN.stat().st_size / 1024 / 1024:.1f} MB) -> {REMOTE_BIN}")
        sftp.put(str(LOCAL_BIN), REMOTE_BIN)
        mode = stat.S_IRWXU | stat.S_IRGRP | stat.S_IXGRP | stat.S_IROTH | stat.S_IXOTH
        sftp.chmod(REMOTE_BIN, mode)
        sftp.close()

        _, stdout, stderr = client.exec_command(f"{REMOTE_BIN} version", timeout=30)
        out = stdout.read().decode("utf-8", "replace").strip()
        err = stderr.read().decode("utf-8", "replace").strip()
        print("--- 远端执行 version ---")
        print(out or err)
    finally:
        client.close()
    return 0


def caps() -> int:
    client, _ = connect()
    try:
        _, stdout, stderr = client.exec_command(f"{REMOTE_BIN} caps", timeout=60)
        out = stdout.read().decode("utf-8", "replace")
        err = stderr.read().decode("utf-8", "replace").strip()
        print(out)
        if err:
            print("--- stderr ---")
            print(err)
    finally:
        client.close()
    return 0


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 1
    cmd = sys.argv[1]
    if cmd == "upload":
        return upload()
    if cmd == "caps":
        return caps()
    print(f"未知命令: {cmd}")
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
