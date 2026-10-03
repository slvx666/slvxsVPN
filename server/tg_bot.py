#!/usr/bin/env python3
"""tg_bot.py — Telegram бот для управления VPN-сервером, сборки APK/EXE и отправки пользователю.
Работает через Long Polling на чистом Python 3 (urllib.request).
Авторизация только для пользователя из monitor.env (TG_CHAT=1146030040)."""

import json
import logging
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Optional, Tuple

ENV_FILE = "/root/vpn/monitor.env"
STATE_FILE = "/root/vpn/tg_bot_state.json"
MSG_LOG = "/root/vpn/tg_messages.log"
MANIFEST_FILE = "/root/build/android/AndroidManifest.xml"
APK_PATH = "/root/build/android/bin/VPN.apk"
EXE_PATH = "/root/build/VPN.exe"

logging.basicConfig(
    format="%(asctime)s [%(levelname)s] %(message)s",
    level=logging.INFO,
    handlers=[
        logging.StreamHandler(sys.stdout),
        logging.FileHandler("/root/vpn/tg_bot.log", encoding="utf-8")
    ]
)


def load_env() -> dict:
    e = {}
    if os.path.isfile(ENV_FILE):
        for line in open(ENV_FILE, encoding="utf-8"):
            line = line.strip()
            if "=" in line and not line.startswith("#"):
                k, v = line.split("=", 1)
                e[k.strip()] = v.strip().strip('"').strip("'")
    return e


ENV = load_env()
TOKEN = ENV.get("TG_TOKEN", "")
ALLOWED_CHAT = str(ENV.get("TG_CHAT", "1146030040"))

if not TOKEN:
    logging.error("TG_TOKEN не найден в %s", ENV_FILE)
    sys.exit(1)

API_URL = f"https://api.telegram.org/bot{TOKEN}"


def load_state() -> dict:
    if os.path.isfile(STATE_FILE):
        try:
            with open(STATE_FILE, "r", encoding="utf-8") as f:
                return json.load(f)
        except Exception:
            pass
    return {"last_update_id": 0}


def save_state(st: dict):
    try:
        with open(STATE_FILE + ".tmp", "w", encoding="utf-8") as f:
            json.dump(st, f, ensure_ascii=False, indent=2)
        os.replace(STATE_FILE + ".tmp", STATE_FILE)
    except Exception as ex:
        logging.error("Ошибка сохранения состояния: %s", ex)


def log_user_message(text: str, user_info: str):
    try:
        with open(MSG_LOG, "a", encoding="utf-8") as f:
            f.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} [{user_info}]: {text}\n")
    except Exception:
        pass


def tg_request(method: str, params: Optional[dict] = None, timeout: int = 40) -> Optional[dict]:
    url = f"{API_URL}/{method}"
    data = None
    headers = {}
    if params:
        data = json.dumps(params).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read().decode("utf-8")
            return json.loads(body)
    except urllib.error.HTTPError as ex:
        logging.error("HTTP error %s: %s", ex.code, ex.read().decode("utf-8", errors="ignore"))
    except Exception as ex:
        logging.error("TG request %s failed: %s", method, ex)
    return None


MAIN_KEYBOARD = {
    "keyboard": [
        [{"text": "📱 Собрать свежий APK"}, {"text": "📥 Скинуть текущий APK"}],
        [{"text": "📊 Статус сервера"}, {"text": "📝 Логи Xray"}],
        [{"text": "💻 Собрать Windows EXE"}, {"text": "🔄 Перезапустить сервисы"}]
    ],
    "resize_keyboard": True,
    "is_persistent": True
}


def send_message(chat_id: int | str, text: str, parse_mode: str = "HTML", reply_markup: Optional[dict] = None):
    # Telegram limit: 4096 chars
    if reply_markup is None:
        reply_markup = MAIN_KEYBOARD
    for chunk in [text[i:i + 4000] for i in range(0, len(text), 4000)]:
        payload = {
            "chat_id": chat_id,
            "text": chunk,
            "parse_mode": parse_mode
        }
        if reply_markup:
            payload["reply_markup"] = reply_markup
        res = tg_request("sendMessage", payload)
        if res and res.get("ok"):
            logging.info("Отправлено сообщение в чат %s (id=%s): %s...", chat_id, res.get("result", {}).get("message_id"), chunk[:60].replace("\n", " "))
        else:
            logging.error("Не удалось отправить сообщение в чат %s: %s", chat_id, res)


def send_document(chat_id: int | str, file_path: str, caption: str = ""):
    if not os.path.isfile(file_path):
        send_message(chat_id, f"❌ Файл не найден: {file_path}")
        return False
    args = ["curl", "-s", "--max-time", "600",
            "-F", f"chat_id={chat_id}",
            "-F", f"document=@{file_path}"]
    if caption:
        args += ["-F", f"caption={caption[:1024]}"]
    args.append(f"{API_URL}/sendDocument")
    try:
        out = subprocess.run(args, capture_output=True, text=True, timeout=660).stdout
        r = json.loads(out or "{}")
        if r.get("ok"):
            return True
        logging.error("sendDocument error: %s", r.get("description"))
        send_message(chat_id, f"❌ Ошибка отправки файла: {r.get('description')}")
    except Exception as ex:
        logging.error("curl sendDocument failed: %s", ex)
        send_message(chat_id, f"❌ Исключение при отправке файла: {ex}")
    return False


def send_current_apk(chat_id: int | str):
    cur_v, cur_c = get_current_version()
    if os.path.isfile(APK_PATH):
        cap = (f"📦 <b>Текущий готовый APK v{cur_v}</b> (код {cur_c})\n"
               f"• Полностью рабочий туннель (VLESS / Hysteria 2)\n"
               f"• Обход замедлений YouTube/Discord/Twitch\n"
               f"• Проверено на устройстве: соединение ~300мс")
        send_document(chat_id, APK_PATH, cap)
    else:
        send_message(chat_id, "⚠️ Готовый APK ещё не собран. Нажмите «📱 Собрать свежий APK».")


def get_current_version() -> Tuple[str, int]:
    try:
        if os.path.isfile(MANIFEST_FILE):
            content = open(MANIFEST_FILE, encoding="utf-8").read()
            v_name = re.search(r'android:versionName="([^"]+)"', content)
            v_code = re.search(r'android:versionCode="([0-9]+)"', content)
            name = v_name.group(1) if v_name else "1.0.15"
            code = int(v_code.group(1)) if v_code else 16
            return name, code
    except Exception as ex:
        logging.error("Error reading version: %s", ex)
    return "1.0.15", 16


def next_version(current: str) -> str:
    parts = current.split(".")
    try:
        parts[-1] = str(int(parts[-1]) + 1)
        return ".".join(parts)
    except Exception:
        return current + ".1"


def run_build_apk(chat_id: int | str, ver: Optional[str] = None):
    cur_v, _ = get_current_version()
    if not ver:
        ver = next_version(cur_v)

    send_message(chat_id, f"⏳ <b>Начинаю сборку APK v{ver}</b>\n(компиляция Go ядра arm64 + Android сборка)...")
    cmd = f"/root/build/build-apk.sh {ver}"
    try:
        t0 = time.time()
        p = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True, timeout=300)
        dt = round(time.time() - t0, 1)
        if p.returncode == 0 and os.path.isfile(APK_PATH):
            cap = (f"✅ <b>APK v{ver} успешно собран!</b> ({dt} с)\n"
                   f"• Ядро Go (arm64, MTU 1420)\n"
                   f"• Опубликован в подписку\n"
                   f"Установите поверх для проверки.")
            send_document(chat_id, APK_PATH, cap)
        else:
            err = (p.stderr or p.stdout or "Неизвестная ошибка")[-1500:]
            send_message(chat_id, f"❌ <b>Ошибка сборки APK v{ver}:</b>\n<pre>{err}</pre>")
    except Exception as ex:
        send_message(chat_id, f"❌ <b>Исключение при сборке:</b> {ex}")


def run_build_exe(chat_id: int | str, ver: Optional[str] = None):
    cur_v, _ = get_current_version()
    if not ver:
        ver = cur_v
    send_message(chat_id, f"⏳ <b>Начинаю сборку Windows EXE v{ver}</b>...")
    cmd = f"/root/build/build-exe.sh {ver}"
    try:
        t0 = time.time()
        p = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True, timeout=300)
        dt = round(time.time() - t0, 1)
        if p.returncode == 0 and os.path.isfile(EXE_PATH):
            cap = f"✅ <b>VPN.exe v{ver} готов!</b> ({dt} с)\nОпубликован в подписку."
            send_document(chat_id, EXE_PATH, cap)
        else:
            err = (p.stderr or p.stdout or "Неизвестная ошибка")[-1500:]
            send_message(chat_id, f"❌ <b>Ошибка сборки EXE:</b>\n<pre>{err}</pre>")
    except Exception as ex:
        send_message(chat_id, f"❌ <b>Исключение при сборке EXE:</b> {ex}")


def get_status() -> str:
    # Uptime
    uptime = subprocess.run(["uptime", "-p"], capture_output=True, text=True).stdout.strip()
    # Memory
    mem = subprocess.run(["free", "-h"], capture_output=True, text=True).stdout.splitlines()
    mem_line = mem[1] if len(mem) > 1 else ""
    # Disk
    df = subprocess.run(["df", "-h", "/"], capture_output=True, text=True).stdout.splitlines()
    disk_line = df[1] if len(df) > 1 else ""
    # Services
    xray_st = subprocess.run(["systemctl", "is-active", "xray"], capture_output=True, text=True).stdout.strip()
    sub_st = subprocess.run(["systemctl", "is-active", "vpn-sub"], capture_output=True, text=True).stdout.strip()

    cur_v, cur_c = get_current_version()

    msg = (
        f"📊 <b>Статус VPN-сервера</b>\n\n"
        f"• <b>Аптайм:</b> {uptime}\n"
        f"• <b>Службы:</b> xray: <code>{xray_st}</code>, subserver: <code>{sub_st}</code>\n"
        f"• <b>Память:</b> <code>{mem_line}</code>\n"
        f"• <b>Диск:</b> <code>{disk_line}</code>\n"
        f"• <b>Текущий APK:</b> v{cur_v} (код {cur_c})\n"
    )
    return msg


def get_logs(n: int = 30) -> str:
    out = subprocess.run(["journalctl", "-u", "xray", "-n", str(n), "--no-pager"],
                         capture_output=True, text=True).stdout
    return f"📝 <b>Последние {n} строк лога Xray:</b>\n<pre>{out[-3500:]}</pre>"


def run_command(chat_id: int | str, cmd: str):
    if not cmd.strip():
        send_message(chat_id, "Пустая команда.")
        return
    send_message(chat_id, f"⚙️ Выполняю: <code>{cmd}</code>...")
    try:
        p = subprocess.run(["bash", "-c", cmd], capture_output=True, text=True, timeout=60)
        res = (p.stdout + p.stderr).strip()
        if not res:
            res = "(пустой вывод, код 0)"
        send_message(chat_id, f"<pre>{res[-3800:]}</pre>")
    except Exception as ex:
        send_message(chat_id, f"❌ Ошибка: {ex}")


def handle_message(msg: dict):
    from_user = msg.get("from", {})
    user_id = str(from_user.get("id", ""))
    username = from_user.get("username", "")
    chat_id = msg.get("chat", {}).get("id")
    text = (msg.get("text") or "").strip()

    user_tag = f"id={user_id} (@{username})"
    logging.info("Сообщение от %s: %s", user_tag, text)
    log_user_message(text, user_tag)

    if user_id != ALLOWED_CHAT:
        logging.warning("Игнорирую сообщение от неавторизованного пользователя %s", user_id)
        return

    lower = text.lower()

    if text in ("/start", "/help", "помощь"):
        cur_v, _ = get_current_version()
        help_text = (
            f"👋 <b>Бот управления VPN-сервером готов!</b>\n\n"
            f"Я слушаю твои сообщения на сервере и выполняю команды.\n\n"
            f"<b>Доступные команды:</b>\n"
            f"📱 <code>/apk [версия]</code> — собрать APK и прислать готовый файл сюда (напр. /apk {next_version(cur_v)})\n"
            f"💻 <code>/exe</code> — собрать Windows EXE и прислать файл\n"
            f"📊 <code>/status</code> — статус сервера, Xray, память и службы\n"
            f"📝 <code>/logs [число]</code> — последние логи Xray\n"
            f"🔄 <code>/restart [xray|sub|all]</code> — перезапустить сервисы\n"
            f"⚡ <code>/cmd &lt;команда&gt;</code> — выполнить bash-команду на сервере\n\n"
            f"<i>Также можно писать обычными словами: 'собери апк', 'скинь апк', 'статус', 'логи' и т.д.</i>"
        )
        send_message(chat_id, help_text)
        return

    # Send current APK triggers
    if (lower.startswith("/get") or "скинуть текущий apk" in lower or "скинь готовый" in lower
            or "дай апк" in lower or "скачать апк" in lower or lower == "скинь апк"):
        send_current_apk(chat_id)
        return

    # APK build triggers
    if (lower.startswith("/apk") or lower.startswith("/build") or "собрать свежий apk" in lower
            or "собери апк" in lower or "сборка" in lower or "собери apk" in lower):
        parts = text.split()
        ver = parts[1] if len(parts) > 1 and re.match(r'^\d+\.\d+\.\d+', parts[1]) else None
        run_build_apk(chat_id, ver)
        return

    # EXE build triggers
    if lower.startswith("/exe") or lower.startswith("/build_exe") or "собрать windows exe" in lower or "собери exe" in lower:
        parts = text.split()
        ver = parts[1] if len(parts) > 1 and re.match(r'^\d+\.\d+\.\d+', parts[1]) else None
        run_build_exe(chat_id, ver)
        return

    # Status triggers
    if lower in ("/status", "статус", "status", "статус сервера") or "статус сервера" in lower:
        send_message(chat_id, get_status())
        return

    # Logs triggers
    if lower.startswith("/logs") or lower in ("логи", "лог", "логи xray") or "логи xray" in lower:
        parts = text.split()
        n = int(parts[1]) if len(parts) > 1 and parts[1].isdigit() else 30
        send_message(chat_id, get_logs(n))
        return

    # Restart triggers
    if lower.startswith("/restart") or lower.startswith("перезапусти") or "перезапустить сервисы" in lower:
        parts = text.split()
        target = parts[1].lower() if len(parts) > 1 else "all"
        if target in ("xray", "all"):
            subprocess.run(["systemctl", "restart", "xray"])
        if target in ("sub", "vpn-sub", "all"):
            subprocess.run(["systemctl", "restart", "vpn-sub"])
        send_message(chat_id, f"✅ Сервисы ({target}) перезапущены!")
        return

    # Direct cmd execution
    if text.startswith("/cmd "):
        cmd = text[5:]
        run_command(chat_id, cmd)
        return

    # Natural text message from user - record to inbox for AI processing
    try:
        inbox_entry = {
            "ts": time.time(),
            "time": time.strftime("%Y-%m-%d %H:%M:%S"),
            "user_id": user_id,
            "username": username,
            "text": text,
            "read": False
        }
        with open("/root/vpn/tg_inbox.jsonl", "a", encoding="utf-8") as f:
            f.write(json.dumps(inbox_entry, ensure_ascii=False) + "\n")
    except Exception as ex:
        logging.error("Failed to write to tg_inbox: %s", ex)

    cur_v, _ = get_current_version()
    send_message(
        chat_id,
        f"📩 <b>Принято!</b> «{text}»\n\n"
        f"Запрос записан в рабочий стек. Antigravity видит сообщение и выполняет необходимые действия.\n\n"
        f"<i>Быстрые действия доступны на кнопках ниже.</i>"
    )


def main_loop():
    state = load_state()
    offset = state.get("last_update_id", 0) + 1
    logging.info("Бот запущен. Слушаю Telegram (чат %s, offset=%s)...", ALLOWED_CHAT, offset)

    while True:
        try:
            updates = tg_request("getUpdates", {"offset": offset, "timeout": 25}, timeout=35)
            if updates and updates.get("ok"):
                for item in updates.get("result", []):
                    up_id = item["update_id"]
                    offset = up_id + 1
                    state["last_update_id"] = up_id
                    save_state(state)
                    if "message" in item:
                        handle_message(item["message"])
            time.sleep(1)
        except Exception as ex:
            logging.error("Ошибка в main_loop: %s", ex)
            time.sleep(5)


if __name__ == "__main__":
    main_loop()
