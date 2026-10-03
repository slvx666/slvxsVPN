#!/bin/bash
# pull.sh [app|android|server|all] — забрать код С сервера (если правили там). Перезаписывает локальные файлы.
source "$(dirname "$0")/common.sh"
what="${1:-all}"
case "$what" in
app | all) echo "← app"; ssh "$SERVER" 'cd /root/build && tar czf - --exclude=app/cmd/dumpcfg app' | tar xzf - -C "$ROOT" ;;&
android | all) echo "← android"; ssh "$SERVER" 'cd /root/build/android && tar czf - src res assets AndroidManifest.xml' | tar xzf - -C "$ROOT/android" ;;&
server | all) echo "← server"; ssh "$SERVER" 'cd /root/vpn && tar czf - subserver.py app_update.py monitor.py build.py tg_send.py shaper.sh diag.ps1 game-mode.ps1 lag-monitor.ps1 wifi-fix.ps1 wifi-probe.ps1 zapret-fix.ps1' | tar xzf - -C "$ROOT/server" ;;&
all) scp -q "$SERVER:/root/build/build-apk.sh" "$SERVER:/root/build/build-exe.sh" "$ROOT/build-server/" ;;
app | android | server) ;;
*) die "неизвестно: $what" ;;
esac
echo "готово"
