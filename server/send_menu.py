import json
import urllib.request

e = dict(line.strip().split("=", 1) for line in open("/root/vpn/monitor.env") if "=" in line and not line.startswith("#"))
token = e["TG_TOKEN"].strip('"').strip("'")
chat_id = e["TG_CHAT"].strip('"').strip("'")

kb = {
    "keyboard": [
        [{"text": "📱 Собрать свежий APK"}, {"text": "📥 Скинуть текущий APK"}],
        [{"text": "📊 Статус сервера"}, {"text": "📝 Логи Xray"}],
        [{"text": "💻 Собрать Windows EXE"}, {"text": "🔄 Перезапустить сервисы"}]
    ],
    "resize_keyboard": True,
    "is_persistent": True
}

text = (
    "👋 <b>Связь установлена!</b> Телефон можно спокойно забирать — я на связи здесь в боте 24/7.\n\n"
    "Ниже появились удобные кнопки для управления сервером и сборки свежих APK/EXE в один тап.\n"
    "Пиши любые вопросы и пожелания прямо сюда!"
)

payload = json.dumps({
    "chat_id": chat_id,
    "text": text,
    "parse_mode": "HTML",
    "reply_markup": kb
}).encode("utf-8")

req = urllib.request.Request(
    f"https://api.telegram.org/bot{token}/sendMessage",
    data=payload,
    headers={"Content-Type": "application/json"}
)

with urllib.request.urlopen(req, timeout=15) as resp:
    print("Response:", resp.read().decode("utf-8"))
