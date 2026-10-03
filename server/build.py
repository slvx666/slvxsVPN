#!/usr/bin/env python3
"""Генерирует конфиг Xray, скрипт шейпинга, ссылки и профили Clash из /root/vpn/users.json.
После правки users.json: python3 /root/vpn/build.py && systemctl restart xray vpn-shaper

Протоколы (все на 443, лимиты скорости работают на каждом):
  VLESS + Reality + Vision   TCP 443  — основной, самый быстрый
  VLESS + XHTTP + Reality    TCP 443  — запасной (fallback с основного инбаунда), устойчивее к DPI
  Hysteria2 + Salamander     UDP 443  — для игр/звонков: UDP без «пробок» TCP; прыжки по портам hy2_hop
"""
import json, os, subprocess, urllib.parse
import yaml

BASE = "/root/vpn"
TLS = "/usr/local/etc/xray/tls"  # копия сертификата Let's Encrypt, её обновляет acme.sh (reloadcmd)
XHTTP_LOCAL = 10444              # внутренний порт XHTTP (сюда fallback с 443)
cfg = json.load(open(f"{BASE}/users.json"))
users = cfg["users"]
S = cfg["server"]

# IP в ссылках должен совпадать с IP сервера — иначе не работает ни подписка, ни ключи
local_ips = subprocess.run(["hostname", "-I"], capture_output=True, text=True).stdout.split()
if S not in local_ips:
    print(f"ВНИМАНИЕ: server={S} не найден среди адресов сервера {local_ips}. "
          f"Если IP сменился — поправьте users.json и перевыпустите сертификат (см. README.txt).")

# routeOnly=False: сервер сам резолвит домен из SNI/Host — берёт ближайшие к Франкфурту узлы CDN
# (а не российские/подменённые IP из DNS клиента) и выходит по IPv6, где он есть (геолокация — Европа)
sniffing = {"enabled": True, "destOverride": ["http", "tls", "quic"], "routeOnly": False,
            "domainsExcluded": ["courier.push.apple.com"]}
reality = {"show": False, "target": f"{cfg['sni']}:443", "xver": 0,
           "serverNames": [cfg["sni"]], "privateKey": cfg["privateKey"],
           "shortIds": [""] + [u["shortId"] for u in users]}

xray = {
    "log": {"loglevel": "warning", "access": "none"},
    "api": {"tag": "api", "services": ["StatsService"]},
    "stats": {},
    "policy": {
        "levels": {"0": {"handshake": 4, "connIdle": 300, "uplinkOnly": 2, "downlinkOnly": 5,
                         "statsUserUplink": True, "statsUserDownlink": True, "bufferSize": 512}},
        # статистику по инбаундам не включать: в Xray 26.3 она оборачивает соединение, и Hysteria2 теряет
        # пользователя -> не работают лимиты скорости и статистика. Нужна только статистика по пользователям.
        "system": {},
    },
    "inbounds": [
        {"tag": "api-in", "listen": "127.0.0.1", "port": 10085, "protocol": "dokodemo-door",
         "settings": {"address": "127.0.0.1"}},
        {
            "tag": "vless-reality", "listen": "0.0.0.0", "port": cfg["port"], "protocol": "vless",
            "settings": {
                "clients": [{"id": u["uuid"], "email": u["name"], "flow": "xtls-rprx-vision"} for u in users],
                "decryption": "none",
                # всё, что прошло Reality, но не является VLESS (т.е. XHTTP-клиенты), уходит на XHTTP-инбаунд
                "fallbacks": [{"dest": XHTTP_LOCAL, "xver": 0}],
            },
            "streamSettings": {
                "network": "tcp", "security": "reality", "realitySettings": reality,
                "sockopt": {"tcpFastOpen": True, "tcpcongestion": "bbr"},
            },
            "sniffing": sniffing,
        },
        {
            "tag": "vless-xhttp", "listen": "127.0.0.1", "port": XHTTP_LOCAL, "protocol": "vless",
            "settings": {"clients": [{"id": u["uuid"], "email": u["name"]} for u in users], "decryption": "none"},
            "streamSettings": {"network": "xhttp", "security": "none",
                               "xhttpSettings": {"path": cfg["xhttp_path"]}},
            "sniffing": sniffing,
        },
        {
            "tag": "hysteria2", "listen": "0.0.0.0", "port": cfg["port"], "protocol": "hysteria",
            "settings": {"version": 2, "clients": [{"auth": u["uuid"], "email": u["name"]} for u in users]},  # в 26.3 — "clients"
            "streamSettings": {
                "network": "hysteria", "security": "tls",
                "tlsSettings": {"alpn": ["h3"], "certificates": [
                    {"certificateFile": f"{TLS}/fullchain.pem", "keyFile": f"{TLS}/key.pem"}]},
                "hysteriaSettings": {"version": 2, "udpIdleTimeout": 60},
                "finalmask": {
                    "udp": [{"type": "salamander", "settings": {"password": cfg["hy2_obfs"]}}],
                    # BBR вместо Brutal: Brutal «долбит» фиксированной скоростью и при потерях раздувает очереди → пинг скачет
                    "quicParams": {"congestion": "bbr"},
                },
            },
            "sniffing": sniffing,
        },
    ],
    "outbounds": [
        {"tag": "direct", "protocol": "freedom", "settings": {"domainStrategy": "AsIs"},
         "streamSettings": {"sockopt": {"tcpFastOpen": True}}},
        {"tag": "block", "protocol": "blackhole"},
    ] + [
        {"tag": f"lim-{u['name']}", "protocol": "freedom", "settings": {"domainStrategy": "AsIs"},
         "streamSettings": {"sockopt": {"mark": u["mark"], "tcpFastOpen": True}}}
        for u in users if u["mark"]
    ],
    "dns": {"servers": ["localhost"]},
    "routing": {
        "domainStrategy": "IPIfNonMatch",
        "rules": [
            {"inboundTag": ["api-in"], "outboundTag": "api"},
            {"ip": ["geoip:private"], "outboundTag": "block"},
            {"protocol": ["bittorrent"], "outboundTag": "block"},
        ] + [
            {"user": [u["name"]], "outboundTag": f"lim-{u['name']}"} for u in users if u["mark"]
        ] + [{"network": "tcp,udp", "outboundTag": "direct"}],
    },
}
json.dump(xray, open("/usr/local/etc/xray/config.json", "w"), indent=2)

# --- шейпинг: HTB на ens3 (egress) и ifb0 (ingress), по классу на каждого ограниченного юзера
# + прыжки по портам Hysteria2: UDP hy2_hop -> 443
lim = [u for u in users if u["mark"]]
hop = cfg["hy2_hop"].replace("-", ":")
sh = ["#!/bin/bash", "# сгенерировано build.py", "DEV=ens3", "IFB=ifb0",
      "stop() {", "  tc qdisc del dev $DEV root 2>/dev/null; tc qdisc del dev $DEV ingress 2>/dev/null",
      "  tc qdisc del dev $IFB root 2>/dev/null; ip link set $IFB down 2>/dev/null",
      "  for T in iptables ip6tables; do",
      "    $T -t mangle -D OUTPUT -j VPN_MARK 2>/dev/null; $T -t mangle -F VPN_MARK 2>/dev/null; $T -t mangle -X VPN_MARK 2>/dev/null",
      "    $T -t nat -D PREROUTING -j VPN_HOP 2>/dev/null; $T -t nat -F VPN_HOP 2>/dev/null; $T -t nat -X VPN_HOP 2>/dev/null",
      "  done",
      "}", 'if [ "$1" = stop ]; then stop; exit 0; fi', "stop", "set -e",
      "modprobe ifb numifbs=1; modprobe act_connmark; modprobe act_mirred; modprobe cls_fw",
      "ip link show $IFB >/dev/null 2>&1 || ip link add $IFB type ifb", "ip link set $IFB up",
      "for T in iptables ip6tables; do",
      "  $T -t mangle -N VPN_MARK; $T -t mangle -A OUTPUT -j VPN_MARK",
      "  $T -t mangle -A VPN_MARK -m mark ! --mark 0 -j CONNMARK --save-mark",
      "  $T -t nat -N VPN_HOP; $T -t nat -A PREROUTING -j VPN_HOP",
      f"  $T -t nat -A VPN_HOP -i $DEV -p udp --dport {hop} -j REDIRECT --to-ports {cfg['port']}",
      "done",
      "for D in $DEV $IFB; do",
      "  tc qdisc add dev $D root handle 1: htb default 10 r2q 1000",
      "  tc class add dev $D parent 1: classid 1:1 htb rate 10gbit ceil 10gbit burst 1m cburst 1m",
      "  tc class add dev $D parent 1:1 classid 1:10 htb rate 9gbit ceil 10gbit burst 1m cburst 1m",
      "  tc qdisc add dev $D parent 1:10 handle 10: fq_codel",
      ]
for u in lim:
    m, r = u["mark"], u["limit_mbit"]
    sh += [f"  tc class add dev $D parent 1:1 classid 1:{m} htb rate {r}mbit ceil {r}mbit burst 256k cburst 256k  # {u['name']}",
           f"  tc qdisc add dev $D parent 1:{m} handle {m}: fq_codel",
           f"  tc filter add dev $D parent 1: protocol all prio 1 handle {m} fw classid 1:{m}"]
sh += ["done",
       "tc qdisc add dev $DEV handle ffff: ingress",
       "tc filter add dev $DEV parent ffff: protocol all prio 1 u32 match u32 0 0 action connmark action mirred egress redirect dev $IFB",
       ""]
open(f"{BASE}/shaper.sh", "w").write("\n".join(sh))


# --- ссылки (для Happ, v2rayN, v2RayTun, Hiddify и т.п.)
def speed(u):
    return "безлимит" if not u["mark"] else f"{u['limit_mbit']} Мбит/с"


def uri(scheme, u, params, label):
    return (f"{scheme}://{u['uuid']}@{S}:{cfg['port']}?{urllib.parse.urlencode(params)}"
            f"#{urllib.parse.quote(label, safe='')}")


links = {}
for u in users:
    real = {"security": "reality", "encryption": "none", "sni": cfg["sni"], "fp": "chrome",
            "pbk": cfg["publicKey"], "sid": u["shortId"], "spx": "/"}
    links[u["name"]] = {
        "reality": uri("vless", u, {"type": "tcp", "flow": "xtls-rprx-vision", **real},
                       f"🇩🇪 VLESS Reality · {speed(u)}"),
        "xhttp": uri("vless", u, {"type": "xhttp", "path": cfg["xhttp_path"], "mode": "auto", **real},
                     f"🇩🇪 XHTTP (запасной) · {speed(u)}"),
        "hy2": uri("hysteria2", u, {"sni": S, "obfs": "salamander", "obfs-password": cfg["hy2_obfs"],
                                    "insecure": 0},
                   f"🇩🇪 Hysteria2 (игры) · {speed(u)}"),
    }
json.dump(links, open(f"{BASE}/links.json", "w"), indent=2, ensure_ascii=False)


# --- готовые профили для Clash Verge Rev / mihomo (ПК): автопереключение, РФ напрямую, UDP отдельно
P_REAL, P_XHTTP, P_HY2 = "🇩🇪 VLESS Reality", "🇩🇪 XHTTP", "🇩🇪 Hysteria2"
G_VPN, G_AUTO, G_GAME = "🌍 VPN", "⚡ Авто", "🎮 Игры и звонки"
PRIVATE = ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16",
           "100.64.0.0/10", "224.0.0.0/4", "fc00::/7", "fe80::/10"]


# сервисы для опции пользователя zapret_direct: напрямую (российский IP, замедление обходит zapret на ПК).
# group: своя группа «напрямую, а если не работает — VPN»; check — адрес автопроверки прямого доступа
# (zapret перестал справляться -> проверка не проходит -> сервис сам уходит в VPN, потом возвращается).
ZAPRET_SETS = {
    "youtube": {"group": "▶️ YouTube", "check": "https://www.youtube.com/generate_204",  # без рекламы: в РФ её нет
                "rules": ["GEOSITE,youtube"]},
    "twitch": {"group": "📺 Twitch", "check": "https://www.twitch.tv/robots.txt", "rules": ["GEOSITE,twitch"]},
    "discord": {"group": "💬 Discord", "check": "https://discord.com/api/v9/gateway", "rules": ["GEOSITE,discord"]},
    # Steam — только раздача игр и обновлений (не заблокирована, российские CDN быстрее); вход, магазин,
    # игровые серверы — через VPN
    "steam-downloads": {"group": None, "rules": [
        "DOMAIN-SUFFIX,steamcontent.com", "DOMAIN-SUFFIX,steampipe.akamaized.net", "DOMAIN-SUFFIX,steamcdn-a.akamaihd.net",
        "DOMAIN,client-update.akamai.steamstatic.com", "DOMAIN,client-update.fastly.steamstatic.com",
        "DOMAIN,client-update.steamstatic.com", "DOMAIN,clientconfig.akamai.steamstatic.com"]},
}


def clash(u):
    reality_opts = {"public-key": cfg["publicKey"], "short-id": u["shortId"]}
    common = {"server": S, "port": cfg["port"], "uuid": u["uuid"], "udp": True, "tls": True,
              "servername": cfg["sni"], "client-fingerprint": "chrome", "reality-opts": reality_opts}
    return {
        # ipv6=True: TUN перехватывает и IPv6, иначе IPv6-трафик (YouTube, Apple...) уходит мимо VPN.
        # AAAA-записи при этом не отдаём (dns.ipv6=False) — приложения идут по IPv4.
        "mixed-port": 7897, "allow-lan": False, "mode": "rule", "log-level": "warning", "ipv6": True,
        "unified-delay": True, "tcp-concurrent": True, "find-process-mode": "strict",
        "profile": {"store-selected": True, "store-fake-ip": True},
        "geo-auto-update": True, "geo-update-interval": 72,
        "geox-url": {k: f"https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/{f}"
                     for k, f in (("geoip", "geoip.dat"), ("geosite", "geosite.dat"), ("mmdb", "country.mmdb"))},
        "tun": {"enable": True, "stack": "mixed", "auto-route": True, "auto-detect-interface": True,
                "strict-route": True, "dns-hijack": ["any:53", "tcp://any:53"]},
        "sniffer": {"enable": True, "force-dns-mapping": True, "parse-pure-ip": True,
                    "override-destination": False,
                    "sniff": {"HTTP": {"ports": [80, "8080-8880"]}, "TLS": {"ports": [443, 8443]},
                              "QUIC": {"ports": [443, 8443]}},
                    "skip-domain": ["Mijia Cloud", "+.push.apple.com"]},
        "dns": {
            "enable": True, "ipv6": False, "enhanced-mode": "fake-ip", "fake-ip-range": "198.18.0.1/16",
            "fake-ip-filter": ["*.lan", "*.local", "*.localdomain", "+.msftconnecttest.com", "+.msftncsi.com",
                               "time.*.com", "ntp.*.com", "+.pool.ntp.org", "+.stun.*.*", "+.stun.*.*.*",
                               "geosite:category-ru"],
            "respect-rules": True,
            "default-nameserver": ["77.88.8.8", "1.1.1.1"],
            "proxy-server-nameserver": ["77.88.8.8", "1.1.1.1"],
            "direct-nameserver": ["77.88.8.8", "77.88.8.1"],
            "nameserver": ["https://1.1.1.1/dns-query", "https://8.8.8.8/dns-query"],
            # российские домены резолвим Яндексом — получаем ближайшие к вам узлы, а не европейские
            "nameserver-policy": {k: ["77.88.8.8", "77.88.8.1"]
                                  for k in ("geosite:category-ru", "+.ru", "+.su", "+.xn--p1ai")},
        },
        "proxies": [
            {"name": P_REAL, "type": "vless", "network": "tcp", "flow": "xtls-rprx-vision", **common},
            {"name": P_XHTTP, "type": "vless", "network": "xhttp", "alpn": ["h2"],
             "xhttp-opts": {"path": cfg["xhttp_path"]}, **common},
            # без прыжков портов: в mihomo после прыжка теряются UDP-пакеты (проверено: 50-75% потерь),
            # без прыжков — 0% потерь. Прыжки корректно работают только в клиентах на ядре Xray.
            {"name": P_HY2, "type": "hysteria2", "server": S, "port": cfg["port"], "password": u["uuid"], "obfs": "salamander", "obfs-password": cfg["hy2_obfs"],
             "sni": S, "alpn": ["h3"], "udp": True},
        ],
        "proxy-groups": [
            {"name": G_VPN, "type": "select", "proxies": [G_AUTO, P_REAL, P_XHTTP, P_HY2]},
            {"name": G_AUTO, "type": "fallback", "proxies": [P_REAL, P_XHTTP, P_HY2],
             "url": "https://www.gstatic.com/generate_204", "interval": 60, "timeout": 3000,
             "max-failed-times": 2, "lazy": False},
            # игры и звонки: весь UDP. Hysteria2 здесь нет — в mihomo (Clash) она теряет UDP-пакеты >1300 байт
            # (проверено: клиент Xray с тем же сервером проходит все размеры) — лобби и старт матча ломаются
            {"name": G_GAME, "type": "select", "proxies": [G_AUTO, P_REAL, P_XHTTP]},
        ] + [
            {"name": ZAPRET_SETS[g]["group"], "type": "fallback", "proxies": ["DIRECT", G_VPN],
             "url": ZAPRET_SETS[g]["check"], "interval": 90, "timeout": 5000, "max-failed-times": 2, "lazy": False}
            for g in u.get("zapret_direct", []) if ZAPRET_SETS[g]["group"]
        ],
        "rules": [f"IP-CIDR,{S}/32,DIRECT,no-resolve", "GEOSITE,private,DIRECT"]
                 + [f"IP-CIDR{'6' if ':' in c else ''},{c},DIRECT,no-resolve" for c in PRIVATE]
                 + ["DOMAIN-SUFFIX,ru,DIRECT", "DOMAIN-SUFFIX,su,DIRECT", "DOMAIN-SUFFIX,xn--p1ai,DIRECT",
                    "GEOSITE,category-ru,DIRECT",
                    # QUIC через туннель не пускаем: браузер сразу уходит на TCP — через VLESS это быстрее и стабильнее
                    "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT",
                 ] + [
                    # zapret_direct: заблокированные в РФ сервисы, которым российский IP не мешает, идут напрямую —
                    # замедление обходит zapret на ПК (он должен работать только на реальном адаптере — zapret-fix.ps1)
                    f"{r},{ZAPRET_SETS[g]['group'] or 'DIRECT'}" for g in u.get("zapret_direct", []) for r in ZAPRET_SETS[g]["rules"]
                 ] + [
                    # российские IP напрямую — только для UDP (игровые серверы). Для TCP нельзя: у YouTube, Apple,
                    # TikTok есть CDN-узлы внутри РФ с российскими IP — они ушли бы напрямую и попали под замедление
                    "AND,((NETWORK,UDP),(GEOIP,RU,no-resolve)),DIRECT",
                    f"NETWORK,UDP,{G_GAME}",
                    f"MATCH,{G_VPN}"],
    }


os.makedirs(f"{BASE}/clash", exist_ok=True)
for u in users:
    with open(f"{BASE}/clash/{u['name']}.yaml", "w") as f:
        f.write(f"# Профиль Clash Verge Rev / mihomo для {u['name']} ({speed(u)}) — сгенерирован build.py\n")
        yaml.safe_dump(clash(u), f, allow_unicode=True, sort_keys=False, width=200)

# --- список подписок для раздачи
txt = []
for u in users:
    txt += [f"### {u['name']} — {speed(u).upper()}",
            f"Подписка: https://{S}:{cfg['sub_port']}/sub/{u['token']}"]
    txt += [f"  {k:8}{v}" for k, v in links[u["name"]].items()] + [""]
with open(f"{BASE}/SUBSCRIPTIONS.txt", "w") as f:
    f.write("\n".join(txt))
print("ok:", len(users), "users,", len(lim), "limited")
