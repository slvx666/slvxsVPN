#!/usr/bin/env python3
"""tg_send.py <файл> [подпись] — отправить файл пользователю через бота (токен/чат из monitor.env).
Telegram: подпись до 1024 символов, файл до 50 МБ."""
import json, subprocess, sys


def env():
    e = {}
    for line in open("/root/vpn/monitor.env"):
        if "=" in line and not line.startswith("#"):
            k, v = line.strip().split("=", 1)
            e[k] = v.strip().strip('"')
    return e


e = env()
path = sys.argv[1]
cap = sys.argv[2] if len(sys.argv) > 2 else ""
args = ["curl", "-s", "--max-time", "300", "-F", "chat_id=" + e["TG_CHAT"], "-F", "document=@" + path]
if cap:
    args += ["-F", "caption=" + cap[:1024]]
out = subprocess.run(args + ["https://api.telegram.org/bot" + e["TG_TOKEN"] + "/sendDocument"],
                     capture_output=True, text=True).stdout
r = json.loads(out or "{}")
print("ok" if r.get("ok") else "error: " + str(r.get("description")))
