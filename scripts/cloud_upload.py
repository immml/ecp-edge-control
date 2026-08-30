#!/usr/bin/env python
# 临时 SFTP 上传工具：Ecp cloud upload（密码经 env）
import os, sys, paramiko, posixpath

host = os.environ.get("ECP_HOST", "38.76.180.246")
user = os.environ.get("ECP_USER", "root")
pw = os.environ.get("ECP_PASS", "")
local, remote = sys.argv[1], sys.argv[2]

cli = paramiko.SSHClient()
cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
cli.connect(host, port=22, username=user, password=pw, timeout=20, banner_timeout=20)
sftp = cli.open_sftp()
remote_dir = posixpath.dirname(remote)
try:
    sftp.mkdir(remote_dir)
except Exception:
    pass
sftp.put(local, remote)
st = sftp.stat(remote)
print(f"uploaded {posixpath.basename(local)} -> {remote} ({st.st_size} bytes)")
sftp.close()
cli.close()