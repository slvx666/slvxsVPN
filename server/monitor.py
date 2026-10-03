#!/usr/bin/env python3
"""Мониторинг VPN-сервера с уведомлениями в Telegram. Запускается таймером vpn-monitor.timer каждые 15 минут.
Пишет только при поломке и при восстановлении; раз в день (после 12:00 МСК) — короткий отчёт «всё работает».
Проверки: службы, IP сервера, сертификат, выдача подписок, каждый протокол реальным подключением (TCP и UDP),
доступность сервера из России (check-host.net: Москва x2, Петербург), место на диске.
Настройки — /root/vpn/monitor.env (TG_TOKEN, TG_CHAT; chat id берётся сам после /start у бота).
Ручной запуск: python3 /root/vpn/monitor.py [--report]   (--report — прислать отчёт сейчас)
"""
import base64, datetime, json, os, shutil, socket, ssl, struct, subprocess, sys, tempfile, time
import urllib.parse, urllib.request
from zoneinfo import ZoneInfo

BASE = "/root/vpn"
ENV = f"{BASE}/monitor.env"
STATE = f"{BASE}/monitor-state.json"
MSK = ZoneInfo("Europe/Moscow")
cfg = json.load(open(f"{BASE}/users.json"))
U = cfg["users"][0]          # проверяем протоколы от имени первого пользователя (трафик копеечный)
S = cfg["server"]


def env():
    e = {}
    for line in open(ENV):
        if "=" in line:
            k, v = line.strip().split("=", 1); e[k] = v
    return e


def http(url, data=None, ua="vpn-monitor", timeout=15, insecure=False):
    req = urllib.request.Request(url, data=data, headers={"User-Agent": ua, "Accept": "application/json"})
    ctx = ssl._create_unverified_context() if insecure else None
    with urllib.request.urlopen(req, timeout=timeout, context=ctx) as r:
        return r.status, r.read()


def tg(text):
    e = env()
    if not e.get("TG_CHAT"):
        try:  # chat id — из первого сообщения боту (/start)
            _, b = http(f"https://api.telegram.org/bot{e['TG_TOKEN']}/getUpdates")
            chats = [u["message"]["chat"]["id"] for u in json.loads(b)["result"] if "message" in u]
            if chats:
                e["TG_CHAT"] = str(chats[-1])
                with open(ENV, "a") as f:
                    f.write(f"TG_CHAT={e['TG_CHAT']}\n")
        except Exception as ex:
            print("getUpdates:", ex)
    if not e.get("TG_CHAT"):
        print("нет TG_CHAT — напишите боту /start\n" + text); return False
    data = urllib.parse.urlencode({"chat_id": e["TG_CHAT"], "text": text, "parse_mode": "HTML",
                                   "disable_web_page_preview": "true"}).encode()
    try:
        http(f"https://api.telegram.org/bot{e['TG_TOKEN']}/sendMessage", data=data); return True
    except Exception as ex:
        print("sendMessage:", ex); return False


# ---------------- проверки: каждая возвращает (ok, строка для отчёта)
def check_services():
    bad = [s for s in ("xray", "nginx", "vpn-sub", "vpn-shaper")
           if subprocess.run(["systemctl", "is-active", s], capture_output=True, text=True).stdout.strip() != "active"]
    return (not bad, "службы работают" if not bad else "не работают службы: " + ", ".join(bad))


def check_ip():
    ips = subprocess.run(["hostname", "-I"], capture_output=True, text=True).stdout.split()
    return (S in ips, f"IP {S} на месте" if S in ips else f"IP сервера сменился! В users.json {S}, у сервера {ips} — см. README")


def check_cert():
    out = []
    ok = True
    for f in ("/etc/nginx/ssl/fullchain.pem", "/usr/local/etc/xray/tls/fullchain.pem"):
        r = subprocess.run(["openssl", "x509", "-in", f, "-noout", "-enddate", "-ext", "subjectAltName"],
                           capture_output=True, text=True).stdout
        try:
            end = datetime.datetime.strptime(r.split("notAfter=")[1].split("\n")[0].strip(), "%b %d %H:%M:%S %Y %Z")
            days = (end - datetime.datetime.utcnow()).total_seconds() / 86400
            if days < 2 or S not in r: ok = False
            out.append(f"{days:.1f} дн")
        except Exception:
            ok = False; out.append("не читается")
    return (ok, "сертификат: " + " / ".join(out) + ("" if ok else " — проверьте продление acme.sh"))


def check_subscription():
    base = f"https://{S}:{cfg['sub_port']}/sub/{U['token']}"
    try:
        st, b = http(base, ua="Happ/3.0")
        n = len([l for l in base64.b64decode(b).decode().splitlines() if "://" in l])
        st2, y = http(base, ua="clash-verge/v2")
        ok = st == 200 and n >= 3 and st2 == 200 and b"proxies:" in y
        return (ok, f"подписки отдаются ({n} ссылки + профиль Clash)" if ok else f"подписка: HTTP {st}/{st2}, ссылок {n}")
    except Exception as ex:
        return (False, f"подписка не отдаётся: {ex}")


def xray_client(kind, port):
    real = {"serverName": cfg["sni"], "fingerprint": "chrome", "publicKey": cfg["publicKey"], "shortId": U["shortId"]}
    if kind == "hy2":
        out = {"protocol": "hysteria", "settings": {"version": 2, "address": S, "port": cfg["port"]},
               "streamSettings": {"network": "hysteria", "security": "tls",
                                  "tlsSettings": {"serverName": S, "alpn": ["h3"]},
                                  "hysteriaSettings": {"version": 2, "auth": U["uuid"]},
                                  "finalmask": {"udp": [{"type": "salamander", "settings": {"password": cfg["hy2_obfs"]}}]}}}
    else:
        user = {"id": U["uuid"], "encryption": "none"}
        if kind == "vless": user["flow"] = "xtls-rprx-vision"
        ss = {"network": "tcp", "security": "reality", "realitySettings": real}
        if kind == "xhttp": ss = {"network": "xhttp", "xhttpSettings": {"path": cfg["xhttp_path"]}, "security": "reality", "realitySettings": real}
        out = {"protocol": "vless", "settings": {"vnext": [{"address": S, "port": cfg["port"], "users": [user]}]}, "streamSettings": ss}
    return {"log": {"loglevel": "error"},
            "inbounds": [{"listen": "127.0.0.1", "port": port, "protocol": "socks", "settings": {"udp": True}}],
            "outbounds": [out]}


def udp_via_socks(port):
    t = socket.create_connection(("127.0.0.1", port), timeout=5)
    t.sendall(b"\x05\x01\x00"); t.recv(2); t.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00"); r = t.recv(10)
    u = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); u.settimeout(3)
    q = b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x07example\x03com\x00\x00\x01\x00\x01"
    ok = 0
    for _ in range(3):
        u.sendto(b"\x00\x00\x00\x01" + socket.inet_aton("1.1.1.1") + struct.pack(">H", 53) + q,
                 ("127.0.0.1", struct.unpack(">H", r[8:10])[0]))
        try: u.recv(2048); ok += 1
        except socket.timeout: pass
    t.close(); return ok


def check_protocols():
    res, bad = [], []
    for i, (kind, name) in enumerate((("vless", "VLESS"), ("xhttp", "XHTTP"), ("hy2", "Hysteria2"))):
        port = 21901 + i
        with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as f:
            json.dump(xray_client(kind, port), f); path = f.name
        p = subprocess.Popen(["xray", "run", "-config", path], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            time.sleep(1.5)
            r = subprocess.run(["curl", "-s", "-o", "/dev/null", "--max-time", "10", "-x", f"socks5h://127.0.0.1:{port}",
                                "-w", "%{http_code} %{time_total}", "https://www.gstatic.com/generate_204"],
                               capture_output=True, text=True).stdout.split()
            tcp_ok = r and r[0] == "204"
            udp = udp_via_socks(port) if kind != "xhttp" else None
            ok = tcp_ok and (udp is None or udp >= 2)
            res.append(f"{name} {float(r[1])*1000:.0f}мс" if tcp_ok else f"{name} НЕ РАБОТАЕТ")
            if not ok: bad.append(name + ("" if tcp_ok else " (TCP)") + ("" if udp is None or udp >= 2 else " (UDP)"))
        except Exception as ex:
            bad.append(f"{name} ({ex})"); res.append(f"{name} ошибка")
        finally:
            p.kill(); os.unlink(path)
    return (not bad, "протоколы: " + ", ".join(res) + ("" if not bad else " — сломаны: " + ", ".join(bad)))


def check_russia():
    nodes = ["ru1.node.check-host.net", "ru2.node.check-host.net", "ru3.node.check-host.net"]
    try:
        q = "&".join(f"node={n}" for n in nodes)
        _, b = http(f"https://check-host.net/check-tcp?host={S}:{cfg['port']}&{q}")
        rid = json.loads(b)["request_id"]
        res = {}
        for _ in range(8):
            time.sleep(2)
            _, b = http(f"https://check-host.net/check-result/{rid}"); res = json.loads(b)
            if all(res.get(n) is not None for n in nodes): break
        okn = [n for n in nodes if res.get(n) and res[n][0] and "time" in res[n][0]]
        city = {"ru1": "Москва", "ru2": "Москва-2", "ru3": "Петербург"}
        txt = ", ".join(f"{city[n[:3]]} {res[n][0]['time']*1000:.0f}мс" for n in okn)
        if len(okn) >= 2:
            return (True, f"из России доступен: {txt}")
        return (False, f"из России НЕ доступен порт {cfg['port']} ({len(okn)}/3 узлов) — возможна блокировка IP")
    except Exception as ex:
        return (True, f"проверка из России не удалась ({ex}) — пропущена")   # сбой внешнего сервиса — не тревога


def check_disk():
    u = shutil.disk_usage("/"); pct = u.used * 100 / u.total
    return (pct < 90, f"диск занят на {pct:.0f}%")


CHECKS = [("services", check_services), ("ip", check_ip), ("cert", check_cert), ("sub", check_subscription),
          ("proto", check_protocols), ("russia", check_russia), ("disk", check_disk)]


def main():
    force_report = "--report" in sys.argv
    state = json.load(open(STATE)) if os.path.exists(STATE) else {}
    results = {k: fn() for k, fn in CHECKS}
    for k, (ok, msg) in results.items(): print(("OK  " if ok else "FAIL"), msg)
    prev = state.get("status", {})
    broke = [results[k][1] for k in results if not results[k][0] and prev.get(k, True)]
    fixed = [results[k][1] for k in results if results[k][0] and prev.get(k, True) is False]
    still = [results[k][1] for k in results if not results[k][0] and prev.get(k, True) is False]
    if broke:
        tg("🔴 <b>VPN: проблема</b>\n" + "\n".join("• " + m for m in broke) + ("\n\nЕщё не починено:\n" + "\n".join("• " + m for m in still) if still else ""))
    if fixed:
        tg("🟢 <b>VPN: восстановлено</b>\n" + "\n".join("• " + m for m in fixed))
    now = datetime.datetime.now(MSK); today = now.strftime("%Y-%m-%d")
    if force_report or (now.hour >= 12 and state.get("report_day") != today):
        allok = all(ok for ok, _ in results.values())
        sent = tg(("✅ <b>VPN: всё работает</b>" if allok else "⚠️ <b>VPN: есть проблемы</b>") + f"  ({now:%d.%m %H:%M} МСК)\n"
                  + "\n".join(("• " if ok else "❌ ") + msg for ok, msg in results.values()))
        if sent: state["report_day"] = today       # не отправилось — попробуем в следующий запуск
    state["status"] = {k: v[0] for k, v in results.items()}
    json.dump(state, open(STATE, "w"), ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
