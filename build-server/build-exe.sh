#!/bin/bash
# build-exe.sh <версия> — Windows EXE и публикация в подписку.
set -e
V=$1; [ -z "$V" ] && { echo "usage: $0 1.0.4"; exit 1; }
export PATH=$PATH:/usr/local/go/bin
cd /root/build/app
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o /root/build/VPN.exe ./cmd/vpn
python3 /root/vpn/app_update.py --publish /root/build/VPN.exe $V
