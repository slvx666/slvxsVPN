#!/bin/bash
# send-tg.sh <файл> [подпись] — отправить файл себе в Telegram через бота сервера (до 50 МБ).
source "$(dirname "$0")/common.sh"
f="$1"; cap="$2"
[ -f "$f" ] || die "нет файла: $f"
name="$(basename "$f")"
scp -q "$f" "$SERVER:/tmp/$name" || die "scp"
ssh "$SERVER" "python3 /root/vpn/tg_send.py '/tmp/$name' $(printf '%q' "$cap"); rm -f '/tmp/$name'"
