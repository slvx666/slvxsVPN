#!/usr/bin/env python3
"""Файлы для приложения VPN (ПК/Android), раздаются по подписке: /sub/<token>/file/<имя>.
Запускается таймером vpn-app-update.timer раз в сутки (и вручную после сборки приложений).
  geosite.dat / geoip.dat — урезанные гео-базы (runetfreedom/russia-v2ray-rules-dat): только нужные категории
  zapret-win.zip          — свежий релиз Flowseal/zapret-discord-youtube (встроенный обход DPI на ПК)
  VPN.exe / VPN.apk       — сами приложения (кладёт сборка: app_update.py --publish <файл> <версия>)
Итог — app/manifest.json: {имя: {version, sha256, size}}; subserver.py вставляет ссылки в подписку приложения.
"""
import hashlib, json, os, shutil, subprocess, sys, tempfile, time, urllib.request

BASE = "/root/vpn/app"
FILES = f"{BASE}/files"
MANIFEST = f"{BASE}/manifest.json"
GEOTRIM = "/root/vpn/bin/geotrim"
GEO_URL = "https://github.com/runetfreedom/russia-v2ray-rules-dat/releases/latest/download/"
SITE_CODES = "category-ru,category-gov-ru,ru-available-only-inside,youtube,twitch,discord,private,ru-blocked@ru"
IP_CODES = "ru,private"
ZAPRET_API = "https://api.github.com/repos/Flowseal/zapret-discord-youtube/releases/latest"


def load():
    try:
        return json.load(open(MANIFEST))
    except Exception:
        return {}


def save(m):
    tmp = MANIFEST + ".tmp"
    json.dump(m, open(tmp, "w"), indent=1, ensure_ascii=False)
    os.replace(tmp, MANIFEST)


def sha(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()


def put(m, name, src, version):
    """Кладёт файл в раздачу (атомарно) и обновляет манифест."""
    os.makedirs(FILES, exist_ok=True)
    dst = f"{FILES}/{name}"
    shutil.copyfile(src, dst + ".tmp")
    os.replace(dst + ".tmp", dst)
    m[name] = {"version": version, "sha256": sha(dst), "size": os.path.getsize(dst)}
    print(f"{name}: {version} {m[name]['size']} B")


def get(url, path):
    req = urllib.request.Request(url, headers={"User-Agent": "vpn-app-update"})
    with urllib.request.urlopen(req, timeout=120) as r, open(path, "wb") as f:
        shutil.copyfileobj(r, f)


def update_geo(m):
    with tempfile.TemporaryDirectory() as d:
        get(GEO_URL + "geosite.dat", f"{d}/geosite.dat")
        get(GEO_URL + "geoip.dat", f"{d}/geoip.dat")
        subprocess.run([GEOTRIM, "trim", f"{d}/geosite.dat", f"{d}/site.dat", SITE_CODES], check=True)
        subprocess.run([GEOTRIM, "trim", f"{d}/geoip.dat", f"{d}/ip.dat", IP_CODES], check=True)
        ver = time.strftime("%Y%m%d")
        for name, src in (("geosite.dat", f"{d}/site.dat"), ("geoip.dat", f"{d}/ip.dat")):
            if m.get(name, {}).get("sha256") != sha(src):
                put(m, name, src, ver)


def update_zapret(m):
    req = urllib.request.Request(ZAPRET_API, headers={"User-Agent": "vpn-app-update", "Accept": "application/vnd.github+json"})
    rel = json.load(urllib.request.urlopen(req, timeout=30))
    ver = rel["tag_name"]
    if m.get("zapret-win.zip", {}).get("version") == ver:
        return
    asset = next(a for a in rel["assets"] if a["name"].endswith(".zip"))
    with tempfile.TemporaryDirectory() as d:
        get(asset["browser_download_url"], f"{d}/z.zip")
        # проверка: в архиве должны быть winws.exe и батники стратегий
        names = subprocess.run(["unzip", "-Z1", f"{d}/z.zip"], capture_output=True, text=True).stdout
        if "winws.exe" not in names or "general" not in names:
            raise RuntimeError("в архиве zapret нет winws.exe/general*.bat")
        put(m, "zapret-win.zip", f"{d}/z.zip", ver)


def main():
    m = load()
    if len(sys.argv) == 4 and sys.argv[1] == "--publish":   # --publish <файл> <версия>
        src, ver = sys.argv[2], sys.argv[3]
        put(m, os.path.basename(src), src, ver)
        save(m)
        return
    errors = []
    for fn in (update_geo, update_zapret):
        try:
            fn(m)
        except Exception as ex:
            errors.append(f"{fn.__name__}: {ex}")
    save(m)
    for e in errors:
        print("ОШИБКА", e, file=sys.stderr)
    sys.exit(1 if errors else 0)


if __name__ == "__main__":
    main()
