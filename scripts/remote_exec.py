#!/usr/bin/env python3
"""在边缘节点上远程执行单条命令。

用法（Linux / Git Bash）：
    .venv/tools/Scripts/python.exe scripts/remote_exec.py "uname -a"
    .venv/tools/Scripts/python.exe scripts/remote_exec.py "!apt update"

以 `!` 开头表示用 sudo 执行（密码经 stdin 传入，不进命令行与 history）。
凭据来自 .deploy/ssh/credentials.json（已被 .gitignore 排除）。
"""

import json
import sys
from pathlib import Path

import paramiko

ROOT = Path(__file__).resolve().parent.parent
CRED_FILE = ROOT / ".deploy" / "ssh" / "credentials.json"


def main() -> int:
    if len(sys.argv) < 2:
        print(__doc__)
        return 1

    raw = " ".join(sys.argv[1:]).strip()
    use_sudo = raw.startswith("!")
    cmd = raw[1:].strip() if use_sudo else raw

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

    try:
        if use_sudo:
            stdin, stdout, _ = client.exec_command(
                "sudo -S -p '' bash -lc " + json.dumps(cmd), get_pty=True, timeout=240
            )
            stdin.write(cred["password"] + "\n")
            stdin.flush()
            out = stdout.read().decode("utf-8", "replace")
            # 过滤掉 sudo 回显的密码与提示行
            lines = [
                ln
                for ln in out.splitlines()
                if ln.strip() != cred["password"] and not ln.strip().startswith("[sudo]")
            ]
            print("\n".join(lines))
        else:
            _, stdout, stderr = client.exec_command(cmd, timeout=240)
            out = stdout.read().decode("utf-8", "replace")
            err = stderr.read().decode("utf-8", "replace")
            print(out, end="")
            if err.strip():
                print("--- stderr ---")
                print(err, end="")
    finally:
        client.close()

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
