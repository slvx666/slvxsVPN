#!/bin/bash
# phone.sh — диагностика Android-приложения по USB (adb), работает и с релизной сборкой (≥1.0.15).
#   phone.sh logs            скачать логи ядра в logs/phone-<время>/ (core.log, xray.log, access.log, cache.json)
#   phone.sh debug on|full|off  журнал соединений: on — access.log; full — ещё и подробный лог Xray; off — выкл.
#                            (вступает в силу при следующем подключении VPN)
#   phone.sh install <apk>   поставить APK поверх (данные сохраняются)
#   phone.sh status          версия приложения, сеть, активный VPN
# Логи ядро копирует в Android/data/app.vpn/files каждые 15 с.
source "$(dirname "$0")/common.sh"
D=/sdcard/Android/data/app.vpn/files
"$ADB" get-state >/dev/null 2>&1 || die "телефон не подключён (adb devices)"
case "$1" in
logs)
	out="$ROOT/logs/phone-$(date +%Y%m%d-%H%M%S)"
	mkdir -p "$out"
	# adb pull/ls на Android/data не видит файлы чужого uid, а чтение по имени работает
	for f in core.log xray.log access.log cache.json; do
		"$ADB" exec-out "cat $D/$f 2>/dev/null" > "$out/$f"
		[ -s "$out/$f" ] || rm -f "$out/$f"
	done
	ls -la "$out"
	echo "--- последние события ядра:"
	grep -vE "probe (udp|tcp)" "$out/core.log" 2>/dev/null | tail -15 ;;
debug)
	case "$2" in
	on) "$ADB" shell "mkdir -p $D && echo on > $D/debug" ;;
	full) "$ADB" shell "mkdir -p $D && echo debug > $D/debug" ;;
	off) "$ADB" shell "rm -f $D/debug $D/access.log" ;;
	*) die "debug on|full|off" ;;
	esac
	echo "флаг: $2 (переподключите VPN в приложении)" ;;
install)
	[ -f "$2" ] || die "нет файла: $2"
	"$ADB" install -r "$2" ;;
status)
	"$ADB" shell dumpsys package app.vpn | grep -m1 versionName
	"$ADB" shell dumpsys connectivity | grep -oE "ni\{(VPN|WIFI|MOBILE|CELLULAR) CONNECTED[^}]{0,40}" | sort -u ;;
*) sed -n 2,9p "$0" ;;
esac
