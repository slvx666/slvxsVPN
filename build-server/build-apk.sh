#!/bin/bash
# build-apk.sh <версия>  — ядро Go + APK (arm64), подпись ключом vpn.keystore, публикация в подписку.
set -e
# релиз по умолчанию; отладочная (run-as/логи по adb): DEBUG=1 ./build-apk.sh <версия>
V=$1; [ -z "$V" ] && { echo "usage: $0 1.0.9"; exit 1; }
export PATH=$PATH:/usr/local/go/bin
cd /root/build/app
GOOS=android GOARCH=arm64 CGO_ENABLED=0 nice go build -trimpath -ldflags "-s -w -buildid=" -o /root/build/libvpncore.so ./cmd/androidcore
cd /root/build/android
cp /root/build/libvpncore.so lib/arm64-v8a/
# sing-box — только по запросу (SINGBOX=1): основное ядро Android — общее с ПК (Xray)
rm -f lib/arm64-v8a/libsingbox.so
[ "$SINGBOX" = 1 ] && [ -f /root/build/sing-box-arm64 ] && cp /root/build/sing-box-arm64 lib/arm64-v8a/libsingbox.so
cp /root/build/app/ui/index.html assets/index.html
sed -i "s/<application android:debuggable=\"true\"/<application/" AndroidManifest.xml
[ "$DEBUG" = 1 ] && sed -i "s/<application/<application android:debuggable=\"true\"/" AndroidManifest.xml
BT=/root/build/sdk/android-15 AJ=/root/build/sdk/android-35/android.jar
code=$(grep -o "versionCode=\"[0-9]*\"" AndroidManifest.xml | grep -o "[0-9]*")
sed -i "s/android:versionCode=\"$code\"/android:versionCode=\"$((code+1))\"/; s/android:versionName=\"[^\"]*\"/android:versionName=\"$V\"/" AndroidManifest.xml
# ресурсы (иконки и пр.) — всегда перекомпилируем
rm -rf build/res-c && mkdir -p build/res-c
find res -type f | while read f; do $BT/aapt2 compile "$f" -o build/res-c; done
rm -rf obj gen && mkdir -p obj gen
$BT/aapt2 link -o bin/base.apk -I $AJ --manifest AndroidManifest.xml --java gen --min-sdk-version 24 --target-sdk-version 35 --auto-add-overlay -A assets build/res-c/*.flat
javac -source 8 -target 8 -cp $AJ -d obj $(find src gen -name "*.java") 2>&1 | grep -vE "Note:|warning|deprecat" || true
rm -f bin/classes.dex
$BT/d8 --release --min-api 24 --lib $AJ --output bin $(find obj -name "*.class")
cp bin/base.apk bin/unsigned.apk && ( cd bin && zip -q unsigned.apk classes.dex ) && zip -q bin/unsigned.apk lib/arm64-v8a/*
$BT/zipalign -f -p 4 bin/unsigned.apk bin/aligned.apk
KSPASS=$(cat /root/build/android/keystore.pass | head -n 1)
$BT/apksigner sign --ks vpn.keystore --ks-key-alias vpn --ks-pass "pass:$KSPASS" --key-pass "pass:$KSPASS" --out bin/VPN.apk bin/aligned.apk
$BT/apksigner verify bin/VPN.apk
[ "$NOPUB" = 1 ] || python3 /root/vpn/app_update.py --publish bin/VPN.apk $V
