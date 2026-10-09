#!/bin/bash
# build.sh apk|apk-debug|exe <версия> [--nopub] — отправить код, собрать на сервере, скачать в releases/.
#   apk        релиз APK (подпись ключом на сервере), публикуется в подписку
#   apk-debug  отладочный APK (run-as по adb), НЕ публикуется
#   exe        Windows EXE, публикуется в подписку
#   --nopub    не публиковать в подписку (только собрать и скачать)
# Пример: tools/build.sh apk 1.0.16
source "$(dirname "$0")/common.sh"
kind="$1"; ver="$2"; nopub=""
[ -z "$kind" ] || [ -z "$ver" ] && die "usage: $0 apk|apk-debug|exe <версия> [--nopub]"
[ "$3" = "--nopub" ] && nopub=1

# перед сборкой — локальная проверка, чтобы не гонять сервер впустую
(cd "$ROOT/app" && go vet ./core ./bdpi && go test ./core) || die "go vet/test не прошли"

"$ROOT/tools/push.sh" code >/dev/null || die "push"
mkdir -p "$ROOT/releases"
case "$kind" in
apk)
	ssh "$SERVER" "NOPUB=$nopub /root/build/build-apk.sh $ver" | tail -2 || die "сборка APK"
	scp -q "$SERVER:/root/build/android/bin/VPN.apk" "$ROOT/releases/VPN-$ver.apk"
	echo "→ releases/VPN-$ver.apk" ;;
apk-debug)
	ssh "$SERVER" "DEBUG=1 NOPUB=1 /root/build/build-apk.sh $ver" | tail -2 || die "сборка APK"
	scp -q "$SERVER:/root/build/android/bin/VPN.apk" "$ROOT/releases/VPN-$ver-debug.apk"
	echo "→ releases/VPN-$ver-debug.apk" ;;
exe)
	if [ -n "$nopub" ]; then
		ssh "$SERVER" "cd /root/build/app && PATH=\$PATH:/usr/local/go/bin GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -H windowsgui' -o /root/build/VPN.exe ./cmd/vpn" || die "сборка EXE"
	else
		ssh "$SERVER" "/root/build/build-exe.sh $ver" | tail -1 || die "сборка EXE"
	fi
	scp -q "$SERVER:/root/build/VPN.exe" "$ROOT/releases/VPN-$ver.exe"
	scp -q "$SERVER:/root/build/VPN.exe" "$ROOT/releases/SewrGate-$ver.exe"
	cp -f "$ROOT/releases/SewrGate-$ver.exe" "$ROOT/releases/SewrGate.exe"
	echo "→ releases/SewrGate-$ver.exe" ;;
*) die "неизвестно: $kind" ;;
esac
