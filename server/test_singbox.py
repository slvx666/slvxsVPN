import json
import subprocess
import time

cfg = {
    "log": {"level": "info"},
    "inbounds": [
        {"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2080}
    ],
    "outbounds": [
        {
            "type": "urltest",
            "tag": "auto",
            "outbounds": ["vless-reality", "hysteria2"],
            "url": "https://www.gstatic.com/generate_204",
            "interval": "1m",
            "tolerance": 50
        },
        {
            "type": "vless",
            "tag": "vless-reality",
            "server": "179.254.162.245",
            "server_port": 443,
            "uuid": "72602235-c412-44c5-a611-ba6554d494cf",
            "flow": "xtls-rprx-vision",
            "tls": {
                "enabled": True,
                "server_name": "www.asus.com",
                "utls": {
                    "enabled": True,
                    "fingerprint": "chrome"
                },
                "reality": {
                    "enabled": True,
                    "public_key": "I9rt51YCqsO3eTLiSAHZSgzX4CA04_VDYRGDwAB-uB4",
                    "short_id": "b83b99eb6ed64929"
                }
            }
        },
        {
            "type": "hysteria2",
            "tag": "hysteria2",
            "server": "179.254.162.245",
            "server_port": 443,
            "password": "72602235-c412-44c5-a611-ba6554d494cf",
            "obfs": {
                "type": "salamander",
                "password": "hfVN9tPuV0ODBUnduOU-oIwt"
            },
            "tls": {
                "enabled": True,
                "server_name": "179.254.162.245",
                "insecure": True
            }
        },
        {"type": "direct", "tag": "direct"}
    ],
    "route": {
        "final": "auto"
    }
}

with open("/root/build/test_singbox.json", "w", encoding="utf-8") as f:
    json.dump(cfg, f, indent=2)

print("1. Checking config syntax...")
res = subprocess.run(["/root/go/bin/sing-box", "check", "-c", "/root/build/test_singbox.json"], capture_output=True, text=True)
print("Check exit code:", res.returncode)
if res.stderr:
    print("Stderr:", res.stderr)
if res.stdout:
    print("Stdout:", res.stdout)

if res.returncode == 0:
    print("2. Launching sing-box background test...")
    proc = subprocess.Popen(["/root/go/bin/sing-box", "run", "-c", "/root/build/test_singbox.json"])
    time.sleep(3)
    try:
        print("3. Testing curl through 127.0.0.1:2080 mixed proxy...")
        curl = subprocess.run(["curl", "-s", "-x", "socks5://127.0.0.1:2080", "--max-time", "6", "https://api.ipify.org"], capture_output=True, text=True)
        print("Exit code:", curl.returncode)
        print("IP via sing-box proxy:", curl.stdout.strip())
    finally:
        proc.terminate()
        proc.wait()
