#!/usr/bin/env python
# 在 Pi 上执行命令（带重试，输出完整）——密码经 env ECP_PI_PASS
import os, sys, time, paramiko

host = os.environ.get("ECP_PI_HOST", "192.168.1.5")
user = os.environ.get("ECP_PI_USER", "orangepi")
pw = os.environ.get("ECP_PI_PASS", "orangepi")
cmd = sys.argv[1]

last = None
for attempt in range(3):
    try:
        cli = paramiko.SSHClient()
        cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
        cli.connect(host, port=22, username=user, password=pw, timeout=15,
                    banner_timeout=15, allow_agent=False, look_for_keys=False)
        stdi, so, se = cli.exec_command(cmd, timeout=240)
        out = so.read().decode("utf-8", "replace")
        err = se.read().decode("utf-8", "replace")
        rc = so.channel.recv_exit_status()
        sys.stdout.write(out)
        if err.strip():
            sys.stderr.write(err)
        cli.close()
        sys.stdout.write(f"\n[exit={rc}]\n")
        sys.exit(rc)
    except Exception as e:  # noqa: BLE001
        last = e
        time.sleep(2)
sys.stderr.write(f"SSH FAILED after 3 attempts: {last}\n")
sys.exit(1)