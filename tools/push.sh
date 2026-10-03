#!/bin/bash
# push.sh [app|android|server|all] — отправить локальный код на сервер (по умолчанию app+android).
#   app     → /root/build/app        (Go: ядро, ByeDPI-обвязка, UI, Windows/Android-ядро)
#   android → /root/build/android    (Java, ресурсы, манифест; ключ подписи и SDK остаются на сервере)
#   server  → /root/vpn              (subserver.py и др.; после — перезапуск vpn-sub)
# Правьте код ЗДЕСЬ, а не на сервере: push перезаписывает файлы на сервере.
source "$(dirname "$0")/common.sh"
what="${1:-code}"

push_app() {
	echo "→ app"
	tar czf - -C "$ROOT" --exclude='app/cmd/dumpcfg' app | ssh "$SERVER" 'cd /root/build && tar xzf -'
}
push_android() {
	echo "→ android"
	tar czf - -C "$ROOT/android" src res assets AndroidManifest.xml | ssh "$SERVER" 'cd /root/build/android && tar xzf -'
}
push_server() {
	echo "→ server (/root/vpn)"
	tar czf - -C "$ROOT/server" . | ssh "$SERVER" 'cd /root/vpn && tar xzf - && python3 -m py_compile subserver.py app_update.py monitor.py build.py tg_send.py && systemctl restart vpn-sub && systemctl is-active vpn-sub'
}
push_scripts() {
	echo "→ build-скрипты"
	scp -q "$ROOT/build-server/build-apk.sh" "$ROOT/build-server/build-exe.sh" "$SERVER:/root/build/"
	ssh "$SERVER" 'chmod +x /root/build/build-apk.sh /root/build/build-exe.sh'
}

case "$what" in
code) push_app; push_android ;;
app) push_app ;;
android) push_android ;;
server) push_server ;;
scripts) push_scripts ;;
all) push_app; push_android; push_scripts; push_server ;;
*) die "неизвестно: $what (app|android|server|scripts|all)" ;;
esac
echo "готово"
