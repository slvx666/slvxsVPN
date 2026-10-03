#!/bin/bash
# Общие настройки для скриптов tools/*.sh (Git Bash на Windows).
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVER="root@179.254.162.245"
ADB="${ADB:-$LOCALAPPDATA/Android/Sdk/platform-tools/adb.exe}"
export MSYS_NO_PATHCONV=1 # не превращать /sdcard/... в C:/Program Files/Git/sdcard/...
set -o pipefail            # ошибка сборки на сервере не теряется в «| tail»

die() { echo "ошибка: $*" >&2; exit 1; }
