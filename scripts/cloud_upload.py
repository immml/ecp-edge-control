#!/usr/bin/env python
# 上传文件到云端（SFTP）
import os, sys, paramiko
host = os.environ.get("ECP_HOST", "38.76.180.246")
user = os.environ.get("ECP_USER", "root")
pw = os.environ.get("ECP_PASS", "")
local, remote = sys.argv[1], sys.argv[2]
cli = paramiko.SSHClient()
cli.set_missing_host_key_policy(paramiko.AutoAddPolicy())
cli.connect(host, port=22, username=user, password=pw, timeout=20, banner_timeout=20, allow_agent=False, look_for_keys=False)
sftp = cli.open_sftp()
sftp.put(local, remote)
print(f"uploaded {local} -> {remote}")
sftp.close(); cli.close()
