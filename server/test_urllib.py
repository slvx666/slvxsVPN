import html
import json
import urllib.request
import urllib.error

token = "8991195510:AAEN5YMctn1_cPu5-cRVL9150gtLMuVGXZw"
chat_id = 1146030040

def send(text, parse_mode="HTML"):
    url = f"https://api.telegram.org/bot{token}/sendMessage"
    payload = {"chat_id": chat_id, "text": text}
    if parse_mode:
        payload["parse_mode"] = parse_mode
    data = json.dumps(payload).encode("utf-8")
    headers = {"Content-Type": "application/json"}
    req = urllib.request.Request(url, data=data, headers=headers)
    try:
        with urllib.request.urlopen(req) as resp:
            print("Response:", resp.read().decode())
    except urllib.error.HTTPError as ex:
        print("HTTP Error:", ex.code, ex.read().decode())
    except Exception as ex:
        print("Error:", ex)

send("👋 <b>Привет! Я тебя слышу!</b>\n\nСервер слушает твои сообщения в реальном времени.\nСейчас пришлю готовый обновленный APK с исправлением для мобильного интернета и Windows!", parse_mode="HTML")
