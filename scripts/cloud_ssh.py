#!/usr/bin/env python
# 临时 SSH 工具（健壮版）：读 stdout+stderr，等待退出码；密码经 env
import os, sys, paramiko

host = os.environ.get("ECP_HOST", "38.76.180.246")
user = os.environ.get("ECP_USER", "root")
pw = os.environ.get("ECP_PASS", "")
cmd = sys.argv[1]

cli = paramiko.SSHClient()
cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
cli.connect(host, port=22, username=user, password=pw, timeout=20, banner_timeout=20, allow_agent=False, look_for_keys=False)
chan = cli.get_transport().open_session()
chan.settimeout(120)
chan.exec_command(cmd + "\n")
out_b, err_b = b"", b""
while True:
    if chan.recv_ready():
        out_b += chan.recv(65536)
    if chan.recv_stderr_ready():
        err_b += chan.recv_stderr(65536)
    if chan.exit_status_ready():
        break
    import time
    time.sleep(0.2)
while chan.recv_ready():
    out_b += chan.recv(65536)
while chan.recv_stderr_ready():
    err_b += chan.recv_stderr(65536)
rc = chan.recv_exit_status()
sys.stdout.write(out_b.decode("utf-8", "replace"))
sys.stderr.write(err_b.decode("utf-8", "replace") + f"\n[exit={rc}]\n")
cli.close()