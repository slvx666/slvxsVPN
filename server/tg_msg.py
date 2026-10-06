#!/usr/bin/env python3
import json
import os
import sys
import urllib.request

def load_env():
    e = {}
    if os.path.isfile("/root/vpn/monitor.env"):
        for line in open("/root/vpn/monitor.env", encoding="utf-8"):
            if "=" in line and not line.startswith("#"):
                k, v = line.strip().split("=", 1)
                e[k] = v.strip().strip('"')
    return e

e = load_env()
text = sys.argv[1] if len(sys.argv) > 1 else ""
if not text:
    sys.exit(0)

payload = {
    "chat_id": e.get("TG_CHAT", "1146030040"),
    "text": text,
    "parse_mode": "HTML"
}

req = urllib.request.Request(
    f"https://api.telegram.org/bot{e.get('TG_TOKEN', '')}/sendMessage",
    data=json.dumps(payload).encode("utf-8"),
    headers={"Content-Type": "application/json"}
)

try:
    with urllib.request.urlopen(req, timeout=15) as resp:
        r = json.loads(resp.read().decode())
        print("ok" if r.get("ok") else "error")
except Exception as ex:
    print("error:", ex)
