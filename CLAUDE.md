# VPN-клиенты (Android + Windows) — контекст для работы

Отвечать по-русски. Пользователь в СПб (провайдер Lovitel/Lealta), сервер — VPS в Германии 179.254.162.245
(SSH по ключу этого ПК, `root`). **Сначала прочитать `CONTEXT.md`** — цель, текущее состояние, открытая задача.
Подробная история — `docs/HANDOFF.md`; runbook сервера — `docs/server-runbook.txt`.

## Как устроено
- `app/core` — общее ядро на Xray-core: `xconf.go` (конфиг: TUN, fakedns, маршруты), `engine.go`
  (выбор протокола VLESS/XHTTP/Hysteria2 по доле удачных проверок; сервисы YouTube/Discord напрямую через
  обход DPI с автоподбором стратегии и запоминанием по сети), `switch.go` (переключатели выходов без обрыва).
- `app/bdpi` — ByeDPI (Android): список стратегий и запуск процесса `libbyedpi.so`.
- `app/ui/index.html` — один интерфейс (WebView) для обеих платформ.
- `app/cmd/androidcore` — ядро Android (отдельный процесс, получает TUN fd от Java); `app/cmd/vpn` — Windows
  (WebView2, wintun, zapret/winws, трей).
- `android/` — Java: MainActivity (WebView+мост), VpnServiceImpl, Core, Bus, Prefs.
- `server/subserver.py` — подписка: по UA `VPNApp/` отдаёт JSON-профиль; `APP_SERVICES` — какие сервисы
  через обход, их пробы. Меняется без переустановки приложений (`tools/push.sh server`).
- `go.mod` → `replace github.com/xtls/xray-core => ../xray-src` (с нашими патчами из `patches/`).

## Сборка и проверка
- Локально: `cd app && go vet ./core ./bdpi && go test ./core`; EXE можно собрать локально
  (`GOOS=windows go build -trimpath -ldflags "-s -w -H windowsgui" ./cmd/vpn`).
- APK — только на сервере (SDK, ключ `vpn.keystore`, пароль в `/root/build/android/keystore.pass` — не копировать сюда):
  `tools/build.sh apk <версия>` (релиз, публикуется), `apk-debug` (debuggable, не публикуется).
- Отправить файл пользователю: `tools/send-tg.sh <файл> "подпись"` (только когда он просит).
- Телефон пользователя (TECNO CM7, Android 15) по adb: `tools/phone.sh logs|debug|install|status`.
  Логи ядра в релизе копируются в `/sdcard/Android/data/app.vpn/files` (читать `adb exec-out cat`, `ls`/`pull` не видят).

## Грабли (дорого стоили)
- Провайдер режет TCP с данными в SYN — **никакого tcpFastOpen** в sockopt.
- VLESS/XHTTP к серверу из дома проходят через раз (рвутся новые TCP); Hysteria2 стабильна ~30 Мбит/с.
- YouTube/Chrome шлют **большой ClientHello** (X25519MLKEM768, 2 сегмента): стратегии под маленький (`-s1 -q1`) не
  работают. Рабочие: `-s1 -q1+s -s5+s -Y`, `-s1 -q1+s -s3+s -Y`. Тестировать стратегии клиентом Go (MLKEM), не curl.
- 1.1.1.1:443 у провайдера заблокирован; discord.com заблокирован по IP (только VPN).
- adb shell ходит через активный на телефоне VPN (у пользователя ещё Hiddify/ByeByeDPI/Amnezia) — проверять
  `tools/phone.sh status` перед замерами. ByeDPI под `run-as` не принимает соединения (SELinux).
- speed.cloudflare.com отвечает 429 на частые тесты — мерить на `fsn1-speed.hetzner.com/100MB.bin`.
- В `ui/index.html` не называть функции/переменные именами свойств window (`top`, `name`, `status`…).
- WebView2 на Windows: контроллер создаётся при скрытом окне → `PutIsVisible(true)` в `show()` (иначе чёрный экран).
- Xray 26.3: `policy.system statsInbound` не включать; у Hysteria-инбаунда пользователи в `clients`.
