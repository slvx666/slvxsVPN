# VPN — клиенты Android и Windows

Свой VPN: сервер в Германии (179.254.162.245) и два минималистичных приложения — «Добавить подписку» и одна кнопка.
Здесь — весь код, над которым работаем. Сервер нужен только для сборки APK (SDK и ключ подписи там) и для раздачи.

## Что где

| Папка | Что внутри |
|---|---|
| `app/` | Go-модуль: общее ядро (`core/`), ByeDPI-обвязка (`bdpi/`), интерфейс (`ui/index.html`), ядро Android (`cmd/androidcore`), приложение Windows (`cmd/vpn`) |
| `android/` | Java-часть Android (WebView, VpnService), ресурсы и иконки, манифест |
| `server/` | Скрипты сервера: подписка (`subserver.py` — какие сервисы напрямую/через обход), публикация файлов, мониторинг, бот |
| `build-server/` | Скрипты сборки, которые лежат на сервере (`/root/build/build-*.sh`) |
| `patches/` | Наши 3 правки исходников Xray-core v26.3.27 |
| `xray-src/` | Сами исходники Xray-core с правками (копия, в git не идёт) |
| `docs/` | История работы и runbook сервера |
| `tools/` | Скрипты рабочего процесса (ниже) |
| `releases/` | Готовые APK/EXE |

## Рабочий процесс (Git Bash)

```bash
tools/build.sh apk 1.0.16        # проверка → код на сервер → релиз APK → releases/ и в подписку
tools/build.sh apk-debug 1.0.16  # отладочный APK (не публикуется)
tools/build.sh exe 1.0.9         # Windows EXE (можно собрать и локально: cd app && GOOS=windows go build ./cmd/vpn)
tools/send-tg.sh releases/VPN-1.0.16.apk "подпись"   # себе в Telegram через бота
tools/phone.sh status            # телефон по USB: версия, сеть
tools/phone.sh logs              # логи ядра с телефона → logs/
tools/phone.sh debug on|full|off # журнал соединений (on) / + подробный лог Xray (full)
tools/push.sh server             # выкатить изменения server/ (перезапускает подписку)
tools/pull.sh                    # если что-то правили прямо на сервере — забрать сюда
```

Правьте код **здесь**: `push`/`build` перезаписывают файлы на сервере.

Версия: APK — аргумент `build.sh` (номер сборки растёт сам); в User-Agent — `app/core/profile.go` (`Version`).
