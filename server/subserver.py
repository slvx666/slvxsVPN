#!/usr/bin/env python3
"""Отдаёт подписки: GET /sub/<token>. Слушает 127.0.0.1:8081, снаружи — nginx TLS на :8443.
Clash Verge Rev / mihomo / FlClash — готовый YAML-профиль (clash/<name>.yaml),
остальным VPN-клиентам (Happ, v2rayN, v2RayTun, Hiddify...) — base64(ссылки: VLESS Reality, XHTTP, Hysteria2),
браузеру — страница с кнопками импорта в приложения и QR-кодом."""
import base64, html, json, os, subprocess, time, urllib.parse
import yaml
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

BASE = "/root/vpn"
APP = f"{BASE}/app"  # файлы приложения VPN (app_update.py): гео-базы, zapret, сами приложения

# Сервисы для приложения: заблокированные/замедленные в РФ, но работающие с российским IP.
# bypass — напрямую через встроенный обход DPI (ПК: zapret, Android: ByeDPI), direct — просто напрямую.
# Приложение само проверяет probes и, если так не работает, пускает сервис через VPN.
APP_SERVICES = [
    {"id": "youtube", "mode": "bypass", "domains": ["geosite:youtube"],   # без рекламы: в РФ её не показывают
     "probes": [{"url": "https://www.youtube.com/", "min_bytes": 100000},
                {"url": "https://redirector.googlevideo.com/report_mapping", "min_bytes": 20000}]},
    {"id": "twitch", "mode": "bypass", "domains": ["geosite:twitch"],   # без рекламы VPN-региона
     "probes": [{"url": "https://www.twitch.tv/", "min_bytes": 100000},
                {"url": "https://usher.ttvnw.net/", "min_bytes": 0}]},
    # Discord: сайт, API, CDN — напрямую через обход, если провайдер пропускает; иначе VPN.
    # Голос (discord.media) — всегда VPN (APP_BLOCKED): голосовые серверы в РФ заблокированы по IP.
    {"id": "discord", "mode": "bypass", "domains": ["geosite:discord"],
     "probes": [{"url": "https://discord.com/", "min_bytes": 160000},  # вся страница: обход, «застревающий» на середине, не годится
                {"url": "https://discord.com/api/v9/gateway", "min_bytes": 0},
                {"url": "https://cdn.discordapp.com/embed/avatars/0.png", "min_bytes": 0}]},
    # Steam — только раздача игр/обновлений (российские CDN быстрее); вход, магазин, игры — через VPN
    {"id": "steam", "mode": "direct", "probes": [], "domains": [
        "domain:steamcontent.com", "domain:steampipe.akamaized.net", "domain:steamcdn-a.akamaihd.net",
        "full:client-update.akamai.steamstatic.com", "full:client-update.fastly.steamstatic.com",
        "full:client-update.steamstatic.com", "full:clientconfig.akamai.steamstatic.com"]},
]
# напрямую: российские сайты и сервисы; заблокированные сайты в зоне .ru (ru-blocked-ru) — через VPN
APP_DIRECT = ["domain:ru", "domain:su", "domain:xn--p1ai", "geosite:category-ru", "geosite:category-gov-ru",
              "geosite:ru-available-only-inside"]
APP_BLOCKED = ["geosite:ru-blocked-ru", "domain:discord.media"]
# Android: QUIC этих сервисов — напрямую (TCP через ByeDPI на каждое соединение ждёт повтора ~0.5 с;
# приложение YouTube сначала пробует QUIC). ПК не трогаем: там zapret и TCP быстрые.
APP_QUIC_DIRECT_ANDROID = {"youtube"}


def cert_pin():
    try:
        der = subprocess.run(["openssl", "x509", "-in", "/usr/local/etc/xray/tls/fullchain.pem", "-outform", "der"],
                             capture_output=True).stdout
        import hashlib
        return hashlib.sha256(der).hexdigest()
    except Exception:
        return "865de47fb55fddb470e917d6cee77b38417194e16e1c03b94d7649469324519b"


# Windows: серверы EA/Respawn (Apex и др.) — напрямую (настоящий IP от 77.88.8.8, без FakeDNS и туннеля)
APP_EA_DIRECT = [
    "domain:ea.com",
    "domain:tnt-ea.com",
    "domain:respawn.com",
    "domain:origin.com",
    "domain:eaanticheat.com",
    "domain:ea.pl",
    "domain:apexlegends.com",
    "domain:eacdn.com",
    "domain:eashooters.com",
    "domain:electronicarts.com",
    "domain:steamserver.net",
    "domain:steampowered.com",
    "domain:steamcommunity.com",
    "domain:steamgames.com",
    "domain:valve.net",
    "domain:valvesoftware.com",
    "domain:amazonaws.com",
    "domain:amazon.com",
]


def app_profile(cfg, u, base, ua=""):
    try:
        man = json.load(open(f"{APP}/manifest.json"))
    except Exception:
        man = {}
    files = {n: {"version": v["version"], "sha256": v["sha256"], "url": f"{base}/file/{n}"} for n, v in man.items()}
    direct = list(APP_DIRECT)
    if "(windows)" in ua:
        direct.extend(APP_EA_DIRECT)
    return {
        "v": 1, "name": u["name"], "tariff": "безлимит" if not u["mark"] else f"{u['limit_mbit']} Мбит/с",
        "country": "Германия",
        "server": cfg["server"], "port": cfg["port"], "sni": cfg["sni"], "pbk": cfg["publicKey"],
        "sid": u["shortId"], "uuid": u["uuid"], "xhttp_path": cfg["xhttp_path"],
        "hy2": {"obfs": cfg["hy2_obfs"], "hop": hy2_hop(cfg, ua), "sni": cfg["server"], "pin_sha256": cert_pin()},
        "direct_domains": direct, "blocked_domains": APP_BLOCKED, "services": services_for(ua),
        "files": files,
    }


def hy2_hop(cfg, ua):
    """Прыжки портов Hysteria2 — только для ПК. На мобильном интернете (CGNAT МегаФона) прыжки ломают Hysteria2:
    клиент каждые ~5 с начинает рукопожатие на новом порту, ответы не доходят (замер 2026-10-08, перехват на
    сервере). Без прыжков (только 443/udp) на LTE стабильно. Телефон часто на LTE -> Android без прыжков."""
    if "(android)" in ua:
        return ""
    return cfg["hy2_hop"]


def services_for(ua):
    import copy
    svcs = copy.deepcopy(APP_SERVICES)
    if "(android)" in ua:
        for s in svcs:
            if s["id"] in APP_QUIC_DIRECT_ANDROID:
                s["quic"] = "direct"
    return svcs


def traffic(name):
    try:
        out = subprocess.run(["xray", "api", "statsquery", "--server=127.0.0.1:10085",
                              f"-pattern=user>>>{name}>>>"], capture_output=True, text=True, timeout=3).stdout
        st = {s["name"].split(">>>")[-1]: int(s.get("value", 0)) for s in json.loads(out).get("stat", [])}
        return st.get("uplink", 0), st.get("downlink", 0)
    except Exception:
        return 0, 0


def qr_svg(text):
    try:
        return subprocess.run(["qrencode", "-t", "SVG", "-m", "2", "--svg-path", "-o", "-", text],
                              capture_output=True, text=True, timeout=3).stdout
    except Exception:
        return ""


PAGE = """<!doctype html><html lang="ru"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex">
<title>{title}</title><style>
body{{margin:0;font:16px/1.45 system-ui,-apple-system,sans-serif;background:#111418;color:#e8eaed}}
main{{max-width:460px;margin:0 auto;padding:24px 16px}}h1{{font-size:22px;margin:0 0 4px}}
.sub{{color:#9aa0a6;margin:0 0 20px}}a.b,button{{display:block;width:100%;box-sizing:border-box;margin:0 0 10px;
padding:14px;border:0;border-radius:12px;background:#2b6cf0;color:#fff;font:600 16px system-ui;text-align:center;
text-decoration:none;cursor:pointer}}a.b.alt{{background:#262b33}}a.b.app{{background:#ED93B1;color:#4B1528}}button{{background:#3a4150}}
.qr{{background:#fff;border-radius:12px;padding:12px;margin:18px auto;max-width:260px}}.qr svg{{display:block;width:100%;height:auto}}
code{{display:block;word-break:break-all;background:#1c2027;padding:10px;border-radius:8px;font-size:12px;color:#bdc1c6}}
ol{{padding-left:20px;color:#bdc1c6}}small{{color:#9aa0a6}}</style></head><body><main>
<h1>{title}</h1><p class="sub">Скорость: {speed}</p>
{apps}<a class="b" href="clash://install-config?url={url_q}&amp;name={name_q}">Добавить в Clash Verge (ПК)</a>
<a class="b alt" href="happ://add/{url}">Добавить в Happ</a>
<a class="b alt" href="v2raytun://import/{url}">Добавить в v2RayTun</a>
<a class="b alt" href="hiddify://import/{url}#{name_q}">Добавить в Hiddify</a>
<a class="b alt" href="streisand://import/{url}#{name_q}">Добавить в Streisand</a>
<button onclick="navigator.clipboard.writeText('{url}').then(()=>this.textContent='Скопировано ✓')">Скопировать ссылку подписки</button>
<div class="qr">{qr}</div>
<ol><li>ПК: <b>Clash Verge Rev</b> — профиль с автопереключением протоколов и прямым доступом к российским сайтам.
Телефон: <b>Happ</b> или <b>v2RayTun</b>.</li>
<li>Нажмите кнопку выше — подписка добавится сама. Или отсканируйте QR / вставьте ссылку вручную («+» → из буфера).</li>
<li>Включите VPN. Подписка обновляется автоматически.</li></ol>
<small>Ключ для ручного добавления:</small><code>{key}</code>
</main></body></html>"""


class H(BaseHTTPRequestHandler):
    def user(self):
        """/sub/<token>[/<action>] -> (cfg, user, action) или (cfg, None, None)"""
        cfg = json.load(open(f"{BASE}/users.json"))
        parts = self.path.split("?")[0].strip("/").split("/")
        if not (len(parts) in (2, 3) or (len(parts) == 4 and parts[2] == "file")) or parts[0] != "sub":
            return cfg, None, None
        u = next((u for u in cfg["users"] if u["token"] == parts[1]), None)
        return cfg, u, ("/".join(parts[2:]) if len(parts) > 2 else "")

    def send(self, body, ctype):
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        # приём отчёта диагностики: POST /sub/<token>/diag -> diag/<имя>-<время>.txt
        cfg, u, action = self.user()
        n = int(self.headers.get("Content-Length") or 0)
        if not u or action != "diag" or not 0 < n <= 8 << 20:
            self.send_error(404)
            return
        os.makedirs(f"{BASE}/diag", exist_ok=True)
        with open(f"{BASE}/diag/{u['name']}-{time.strftime('%Y%m%d-%H%M%S')}.txt", "wb") as f:
            f.write(self.rfile.read(n))
        self.send(b"OK\n", "text/plain; charset=utf-8")

    def do_GET(self):
        cfg, u, action = self.user()
        if u and action.startswith("file/"):
            # файлы приложения: гео-базы, zapret, сами приложения (только из манифеста)
            name = action[5:]
            try:
                ok = name in json.load(open(f"{APP}/manifest.json"))
            except Exception:
                ok = False
            if not ok or not os.path.isfile(f"{APP}/files/{name}"):
                self.send_error(404)
                return
            size = os.path.getsize(f"{APP}/files/{name}")
            self.send_response(200)
            self.send_header("Content-Type", "application/vnd.android.package-archive" if name.endswith(".apk")
                             else "application/octet-stream")
            self.send_header("Content-Length", str(size))
            self.send_header("Content-Disposition", f'attachment; filename="{name}"')
            self.end_headers()
            try:
                with open(f"{APP}/files/{name}", "rb") as f:
                    while b := f.read(1 << 20):
                        self.wfile.write(b)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        if not u or action not in ("", "diag.ps1", "diag.yaml", "zapret-fix.ps1", "lag-monitor.ps1", "wifi-fix.ps1", "game-mode.ps1", "wifi-probe.ps1", "speed", "ping"):
            self.send_error(404)
            return
        if action == "ping":
            # замер задержки для lag-monitor.ps1: пустой ответ по уже открытому соединению = 1 RTT
            self.send_response(204)
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            return
        if action == "speed":
            # тест скорости сервер -> клиент без VPN-протокола (обычный HTTPS через nginx): /sub/<token>/speed?bytes=N
            q = urllib.parse.parse_qs(urllib.parse.urlsplit(self.path).query)
            n = min(int((q.get("bytes") or ["25000000"])[0]), 100_000_000)
            chunk = os.urandom(1 << 20)
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(n))
            self.end_headers()
            try:
                while n > 0:
                    k = min(n, len(chunk)); self.wfile.write(chunk[:k]); n -= k
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        base = f"https://{cfg['server']}:{cfg['sub_port']}/sub/{u['token']}"
        if action.endswith(".ps1"):
            ps = open(f"{BASE}/{action}", encoding="utf-8").read()
            for k, v in {"__SERVER__": cfg["server"], "__SUB__": base, "__UP__": f"{base}/diag",
                         "__DIAGPROFILE__": f"{base}/diag.yaml"}.items():
                ps = ps.replace(k, v)
            self.send(ps.encode("utf-8"), "text/plain; charset=utf-8")
            return
        if action == "diag.yaml":
            # тот же профиль, но для теста: без TUN, на своих портах, выбор групп не запоминается
            c = yaml.safe_load(open(f"{BASE}/clash/{u['name']}.yaml", encoding="utf-8"))
            c.update({"mixed-port": 17897, "external-controller": "127.0.0.1:19097", "log-level": "info",
                      "geo-auto-update": False, "find-process-mode": "off", "profile": {"store-selected": False}})
            c["tun"]["enable"] = False
            self.send(yaml.safe_dump(c, allow_unicode=True, sort_keys=False).encode(), "text/yaml; charset=utf-8")
            return
        links = json.load(open(f"{BASE}/links.json"))
        speed = "безлимит" if not u["mark"] else f"{u['limit_mbit']} Мбит/с"
        title = f"🇩🇪 VPN Германия · {speed}"
        ua, accept = self.headers.get("User-Agent", ""), self.headers.get("Accept", "")
        mine = links[u["name"]]
        if ua.startswith("VPNApp/"):
            base = f"https://{cfg['server']}:{cfg['sub_port']}/sub/{u['token']}"
            self.send(json.dumps(app_profile(cfg, u, base, ua), ensure_ascii=False).encode(), "application/json; charset=utf-8")
            return
        if "Mozilla" in ua and "text/html" in accept:
            url = f"https://{cfg['server']}:{cfg['sub_port']}/sub/{u['token']}"
            try:
                man = json.load(open(f"{APP}/manifest.json"))
            except Exception:
                man = {}
            apps = "".join(
                f'<a class="b app" href="{url}/file/{n}">{label}</a>'
                for n, label in (("VPN.apk", "Приложение VPN для Android"), ("VPN.exe", "Приложение VPN для Windows"))
                if n in man)
            if apps:
                apps += ('<p class="sub" style="margin-top:4px">В приложении: «Добавить подписку» → «Вставить» → '
                         '«Готово» (ссылку скопируйте кнопкой ниже). Или любое приложение из списка:</p>')
            body = PAGE.format(apps=apps, title=html.escape(title), speed=speed, url=url, qr=qr_svg(url),
                               url_q=urllib.parse.quote(url, safe=""), name_q=urllib.parse.quote(title),
                               key=html.escape(mine["reality"])).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Cache-Control", "no-store")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if any(k in ua.lower() for k in ("clash", "mihomo", "stash")):
            body, ctype = open(f"{BASE}/clash/{u['name']}.yaml", "rb").read(), "text/yaml; charset=utf-8"
        else:
            body, ctype = base64.b64encode(("\n".join(mine.values()) + "\n").encode()), "text/plain; charset=utf-8"
        up, down = traffic(u["name"])
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("profile-title", "base64:" + base64.b64encode(title.encode()).decode())
        self.send_header("profile-update-interval", "6")
        self.send_header("subscription-userinfo", f"upload={up}; download={down}; total=0; expire=0")
        self.send_header("Content-Disposition", 'attachment; filename="vpn-de"')
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *a):
        pass


ThreadingHTTPServer(("127.0.0.1", 8081), H).serve_forever()
