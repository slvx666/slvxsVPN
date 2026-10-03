import json
import urllib.request

def env():
    e = {}
    for line in open('/root/vpn/monitor.env'):
        if '=' in line and not line.startswith('#'):
            k, v = line.strip().split('=', 1)
            e[k] = v.strip().strip('"')
    return e

e = env()
text = """📋 <b>ОТЧЁТ: Ситуация с портом 443 и стабильностью VPN</b>

1. <b>Серверная часть:</b>
• Процесс Xray на сервере (179.254.162.245:443) работает без единого падения (аптайм более 2 суток, PID 196853).
• Порт 443 слушается корректно: TCP (VLESS Reality + fallback XHTTP) и UDP (Hysteria2 с port hopping 20000-40000).
• Конфликтов портов или падений служб на сервере не зафиксировано.

2. <b>Почему возникали проблемы и задержки в течение дня:</b>
• В РФ ТСПУ/провайдеры применяют эвристику против чистого TLS/Reality на 443 порту — соединения не рвутся сразу, а искусственно замедляются (throttling) или отбрасываются SYN-пакеты.
• Также блокируются стандартные DoH-резолверы (1.1.1.1, 8.8.8.8) и фильтруется TCP при включённом TCP Fast Open.

3. <b>На клиентах (Windows и Android):</b>
• На Windows возникала ошибка таблицы маршрутизации Wintun («Element not found» при установке маршрутов).
• На Android стоял завышенный MTU (8500), из-за чего мобильные операторы (LTE) отбрасывали пакеты с флагом DF больше 1420 байт.

Сейчас я полностью вычищаю эти узкие места: внедряю надёжное добавление маршрутов как в ядре Hiddify/sing-box, фиксирую MTU 1420 и настраиваю адаптивное переключение VLESS Reality / Hysteria2 / XHTTP на нативную скорость 1000 Мбит/с. Отправлю обновлённый APK прямо сюда!"""

url = f"https://api.telegram.org/bot{e['TG_TOKEN']}/sendMessage"
data = json.dumps({'chat_id': e['TG_CHAT'], 'text': text, 'parse_mode': 'HTML'}).encode('utf-8')
req = urllib.request.Request(url, data=data, headers={'Content-Type': 'application/json'})
with urllib.request.urlopen(req, timeout=15) as resp:
    res = json.loads(resp.read().decode('utf-8'))
    print('OK:', res.get('ok'))
