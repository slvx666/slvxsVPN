#!/bin/bash
# disk-guard.sh — держит на диске не меньше 3 ГБ свободного места: сначала сжимает журнал systemd,
# потом обрезает логи Xray (самое старое уходит первым). Запуск из cron раз в 10 минут.
MIN_KB=$((3*1024*1024))
free_kb() { df --output=avail -k / | tail -1; }
[ "$(free_kb)" -ge "$MIN_KB" ] && exit 0
journalctl --vacuum-size=200M >/dev/null 2>&1
[ "$(free_kb)" -ge "$MIN_KB" ] && exit 0
for f in /var/log/xray-access.log /var/log/xray/*.log; do [ -f "$f" ] && : > "$f"; done
rm -f /var/log/xray/*.gz /var/log/xray-access.log.*.gz
[ "$(free_kb)" -ge "$MIN_KB" ] && exit 0
journalctl --vacuum-size=50M >/dev/null 2>&1
