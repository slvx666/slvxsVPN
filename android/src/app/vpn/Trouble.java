package app.vpn;

import android.content.Context;
import android.content.Intent;
import android.content.IntentFilter;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.wifi.WifiInfo;
import android.net.wifi.WifiManager;
import android.os.BatteryManager;
import android.os.Build;
import android.os.PowerManager;
import android.telephony.CellSignalStrength;
import android.telephony.CellSignalStrengthLte;
import android.telephony.SignalStrength;
import android.telephony.TelephonyManager;

import java.io.File;
import java.io.FileOutputStream;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;

/** Журнал проблем trouble.log (Android/data/app.vpn/files): события службы и снимки состояния устройства.
 *  Ядро пишет в тот же файл (строки «[ядро]»). Файл до 3 МБ: старое уходит в trouble.log.1. */
final class Trouble {
    private static final SimpleDateFormat FMT = new SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US);

    static synchronized void log(Context c, String msg) {
        try {
            File dir = c.getExternalFilesDir(null);
            if (dir == null) dir = c.getFilesDir();
            File f = new File(dir, "trouble.log");
            if (f.length() > 3L << 20) {
                File old = new File(dir, "trouble.log.1");
                old.delete();
                f.renameTo(old);
            }
            try (FileOutputStream o = new FileOutputStream(f, true)) {
                o.write((FMT.format(new Date()) + " [прилож] " + msg + "\n").getBytes(StandardCharsets.UTF_8));
            }
        } catch (Exception ignore) {}
    }

    /** Снимок: сеть (Wi-Fi: уровень/частота/скорость; мобильная: оператор и сигнал), батарея, режимы экономии. */
    static String snapshot(Context c) {
        StringBuilder b = new StringBuilder();
        try {
            ConnectivityManager cm = c.getSystemService(ConnectivityManager.class);
            Network n = VpnServiceImpl.physical(cm);
            NetworkCapabilities caps = n == null ? null : cm.getNetworkCapabilities(n);
            if (caps == null) {
                b.append("сеть=НЕТ");
            } else if (caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)) {
                b.append("сеть=wifi");
                try {
                    WifiManager wm = (WifiManager) c.getApplicationContext().getSystemService(Context.WIFI_SERVICE);
                    WifiInfo wi = wm.getConnectionInfo();
                    b.append(" rssi=").append(wi.getRssi()).append(" ").append(wi.getFrequency()).append("МГц")
                            .append(" линк=").append(wi.getLinkSpeed()).append("Мбит");
                } catch (Exception ignore) {}
            } else if (caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR)) {
                b.append("сеть=моб");
                try {
                    TelephonyManager tm = (TelephonyManager) c.getSystemService(Context.TELEPHONY_SERVICE);
                    b.append(" ").append(tm.getNetworkOperatorName());
                    if (Build.VERSION.SDK_INT >= 28) {
                        SignalStrength ss = tm.getSignalStrength();
                        if (ss != null) {
                            b.append(" уровень=").append(ss.getLevel());
                            for (CellSignalStrength cs : ss.getCellSignalStrengths())
                                if (cs instanceof CellSignalStrengthLte) {
                                    CellSignalStrengthLte l = (CellSignalStrengthLte) cs;
                                    b.append(" rsrp=").append(l.getRsrp()).append(" rsrq=").append(l.getRsrq())
                                            .append(" snr=").append(l.getRssnr());
                                }
                        }
                    }
                } catch (Exception ignore) {}
            } else {
                b.append("сеть=другая");
            }
            if (caps != null) {
                b.append(" вниз=").append(caps.getLinkDownstreamBandwidthKbps() / 1000).append("М")
                        .append(" вверх=").append(caps.getLinkUpstreamBandwidthKbps() / 1000).append("М");
            }
        } catch (Exception e) { b.append("сеть=?"); }
        try {
            Intent bat = c.registerReceiver(null, new IntentFilter(Intent.ACTION_BATTERY_CHANGED));
            if (bat != null) {
                int lvl = bat.getIntExtra(BatteryManager.EXTRA_LEVEL, -1), sc = bat.getIntExtra(BatteryManager.EXTRA_SCALE, 100);
                b.append(" батарея=").append(lvl * 100 / Math.max(sc, 1)).append("%");
                if (bat.getIntExtra(BatteryManager.EXTRA_PLUGGED, 0) != 0) b.append("(зарядка)");
            }
            PowerManager pm = (PowerManager) c.getSystemService(Context.POWER_SERVICE);
            b.append(pm.isInteractive() ? " экран=вкл" : " экран=выкл");
            if (pm.isPowerSaveMode()) b.append(" ЭКОНОМИЯ");
            if (pm.isDeviceIdleMode()) b.append(" ДРЁМА");
            if (!pm.isIgnoringBatteryOptimizations(c.getPackageName())) b.append(" батарея-ограничена");
        } catch (Exception ignore) {}
        return b.toString();
    }

    static String ver(Context c) {
        try { return c.getPackageManager().getPackageInfo(c.getPackageName(), 0).versionName; }
        catch (Exception e) { return "?"; }
    }
}
