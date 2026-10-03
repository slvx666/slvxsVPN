import json
import subprocess

cfg = {
    "log": {"level": "info"},
    "inbounds": [
        {"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2081}
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
        {
            "type": "socks",
            "tag": "byedpi",
            "server": "127.0.0.1",
            "server_port": 10801
        },
        {"type": "direct", "tag": "direct"}
    ],
    "route": {
        "rules": [
            {
                "domain_suffix": [
                    "googlevideo.com", "youtube.com", "ytimg.com", "youtu.be", "ggpht.com",
                    "discord.com", "discord.gg", "discordapp.com", "discordapp.net",
                    "twitch.tv", "ttvnw.net", "jtvnw.net"
                ],
                "outbound": "byedpi"
            },
            {
                "domain_suffix": [".ru", ".su", ".xn--p1ai", "vk.com", "yandex.ru", "ya.ru", "gosuslugi.ru", "sberbank.ru", "tinkoff.ru"],
                "outbound": "direct"
            }
        ],
        "final": "auto"
    }
}

with open("/root/build/test_singbox_standalone.json", "w", encoding="utf-8") as f:
    json.dump(cfg, f, indent=2)

res = subprocess.run(["/root/go/bin/sing-box", "check", "-c", "/root/build/test_singbox_standalone.json"], capture_output=True, text=True)
print("Check exit code:", res.returncode)
print("Stderr:", res.stderr)
