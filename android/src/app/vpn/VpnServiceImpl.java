package app.vpn;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.ConnectivityManager;
import android.net.LocalServerSocket;
import android.net.LocalSocket;
import android.net.Network;
import android.net.VpnService;
import android.os.Build;
import android.os.ParcelFileDescriptor;

import java.io.BufferedReader;
import java.io.FileDescriptor;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;

/** Служба VPN: живёт в фоне (foreground service), поэтому продолжает работать, когда окно закрыто крестиком. */
public class VpnServiceImpl extends VpnService {
    public static final String ACTION_START = "start", ACTION_STOP = "stop";
    static final String TUN_ADDR4 = "172.19.0.1", TUN_DNS4 = "172.19.0.2", TUN_ADDR6 = "fdfe:dcba:9876::1";
    static final int MTU = 1420, NOTIF_ID = 1;

    private ParcelFileDescriptor tun;
    private Process core;
    private OutputStream coreIn;
    private LocalServerSocket sock;
    private ConnectivityManager.NetworkCallback netCb;
    private volatile boolean running;

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        String a = intent == null ? ACTION_START : intent.getAction();
        if (ACTION_STOP.equals(a)) { stopAll(); return START_NOT_STICKY; }
        if (!running) start();
        return START_STICKY;
    }

    private void start() {
        running = true;
        Prefs.setWantOn(this, true);
        startForegroundNotif("Подключаюсь…");
        Bus.post("{\"state\":\"connecting\",\"detail\":\"Подбираю лучший путь\"}");
        new Thread(this::bringUp, "vpn-up").start();
    }

    private void bringUp() {
        try {
            Builder b = new Builder()
                    .setSession("VPN")
                    .setMtu(MTU)
                    .addAddress(TUN_ADDR4, 30)
                    .addAddress(TUN_ADDR6, 126)
                    .addDnsServer(TUN_DNS4)
                    .addDnsServer("77.88.8.8")
                    .addRoute("0.0.0.0", 0)
                    .addRoute("::", 0)
                    .setConfigureIntent(PendingIntent.getActivity(this, 0,
                            new Intent(this, MainActivity.class),
                            PendingIntent.FLAG_UPDATE_CURRENT | (Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0)));
            // наш собственный трафик (ядро, подписка, обновления) — мимо туннеля, иначе получится петля
            try { b.addDisallowedApplication(getPackageName()); } catch (Exception ignore) {}
            if (Build.VERSION.SDK_INT >= 29) b.setMetered(false);
            tun = b.establish();
            if (tun == null) { fail("VPN не разрешён"); return; }

            String name = "vpn_tun_" + System.nanoTime();
            sock = new LocalServerSocket(name);

            ProcessBuilder pb = new ProcessBuilder(Core.bin(libDir()).getAbsolutePath(),
                    "run", getFilesDir().getAbsolutePath(), libDir().getAbsolutePath(), name)
                    .redirectErrorStream(false);
            // папка для диагностики (Android/data/app.vpn/files): ядро копирует туда логи, читается по adb без root
            java.io.File diag = getExternalFilesDir(null);
            if (diag != null) pb.environment().put("VPN_DIAG_DIR", diag.getAbsolutePath());
            core = pb.start();
            coreIn = core.getOutputStream();
            captureErr();

            // отдать ядру дескриптор туннеля
            LocalSocket conn = sock.accept();
            conn.setFileDescriptorsForSend(new FileDescriptor[]{tun.getFileDescriptor()});
            conn.getOutputStream().write(new byte[]{1});
            conn.getOutputStream().flush();

            new Thread(this::readState, "vpn-state").start();
            registerNetCallback();
        } catch (Exception e) {
            fail("Не удалось запустить VPN");
        }
    }

    private final StringBuilder errTail = new StringBuilder();

    /** Читаем аварийный вывод ядра (паники, ошибки) — чтобы показать причину, если оно упадёт. */
    private void captureErr() {
        final Process p = core;
        new Thread(() -> {
            try (BufferedReader er = new BufferedReader(new InputStreamReader(p.getErrorStream(), StandardCharsets.UTF_8))) {
                String l;
                while ((l = er.readLine()) != null) {
                    synchronized (errTail) {
                        errTail.append(l).append('\n');
                        if (errTail.length() > 3000) errTail.delete(0, errTail.length() - 3000);
                    }
                }
            } catch (Exception ignore) {}
        }, "vpn-err").start();
    }

    /** Ядро печатает JSON-состояние построчно — пересылаем на экран и в уведомление. */
    private void readState() {
        try (BufferedReader r = new BufferedReader(new InputStreamReader(core.getInputStream(), StandardCharsets.UTF_8))) {
            String line;
            while ((line = r.readLine()) != null) {
                if (!line.startsWith("{")) continue;
                Bus.post(line);
                updateNotif(line);
            }
        } catch (Exception ignore) {}
        if (running) { // ядро неожиданно завершилось — покажем причину из его вывода
            String reason;
            synchronized (errTail) { reason = errTail.toString().trim(); }
            reason = lastLines(reason, 2);
            if (reason.isEmpty()) reason = tailFile(new java.io.File(getFilesDir(), "core.log"), 2);
            if (reason.isEmpty()) { try { reason = "код " + core.exitValue(); } catch (Exception e) { reason = "неизвестно"; } }
            String msg = "Ядро остановилось: " + reason;
            Bus.post("{\"state\":\"off\",\"error\":" + org.json.JSONObject.quote(msg) + "}");
            stopAll();
        }
    }

    private static String lastLines(String s, int n) {
        if (s == null || s.isEmpty()) return "";
        String[] a = s.split("\n");
        StringBuilder b = new StringBuilder();
        for (int i = Math.max(0, a.length - n); i < a.length; i++) {
            if (b.length() > 0) b.append(" | ");
            b.append(a[i].trim());
        }
        String r = b.toString();
        return r.length() > 300 ? r.substring(r.length() - 300) : r;
    }

    private static String tailFile(java.io.File f, int n) {
        try (BufferedReader r = new BufferedReader(new InputStreamReader(new java.io.FileInputStream(f), StandardCharsets.UTF_8))) {
            java.util.ArrayList<String> lines = new java.util.ArrayList<>();
            String l;
            while ((l = r.readLine()) != null) { lines.add(l); if (lines.size() > 50) lines.remove(0); }
            StringBuilder b = new StringBuilder();
            for (int i = Math.max(0, lines.size() - n); i < lines.size(); i++) {
                if (b.length() > 0) b.append(" | ");
                b.append(lines.get(i).trim());
            }
            String s = b.toString();
            return s.length() > 300 ? s.substring(s.length() - 300) : s;
        } catch (Exception e) { return ""; }
    }

    private void toCore(String cmd) {
        try { if (coreIn != null) { coreIn.write((cmd + "\n").getBytes(StandardCharsets.UTF_8)); coreIn.flush(); } }
        catch (Exception ignore) {}
    }

    private void registerNetCallback() {
        ConnectivityManager cm = getSystemService(ConnectivityManager.class);
        if (cm == null) return;
        netCb = new ConnectivityManager.NetworkCallback() {
            private long lastKey = -1, lastAt = 0;
            @Override public void onAvailable(Network n) { changed(n); }
            @Override public void onLost(Network n) { changed(n); }
            private void changed(Network n) {
                long k = n == null ? 0 : n.getNetworkHandle();
                long now = System.currentTimeMillis();
                if (k == lastKey && now - lastAt < 3000) return;
                lastKey = k; lastAt = now;
                toCore("net " + k);
            }
        };
        try { cm.registerDefaultNetworkCallback(netCb); } catch (Exception ignore) {}
    }

    private void fail(String msg) {
        Bus.post("{\"state\":\"off\",\"error\":\"" + msg + "\"}");
        stopAll();
    }

    private void stopAll() {
        running = false;
        Prefs.setWantOn(this, false);
        ConnectivityManager cm = getSystemService(ConnectivityManager.class);
        if (cm != null && netCb != null) { try { cm.unregisterNetworkCallback(netCb); } catch (Exception ignore) {} netCb = null; }
        toCore("stop");
        final Process p = core; core = null; coreIn = null;
        new Thread(() -> {
            try { if (p != null) { p.getOutputStream().close(); if (!p.waitFor(3, java.util.concurrent.TimeUnit.SECONDS)) p.destroyForcibly(); } }
            catch (Exception e) { if (p != null) p.destroyForcibly(); }
        }).start();
        try { if (sock != null) sock.close(); } catch (Exception ignore) {}
        try { if (tun != null) tun.close(); } catch (Exception ignore) {}
        sock = null; tun = null;
        Bus.post("{\"state\":\"off\"}");
        stopForeground(true);
        stopSelf();
    }

    @Override public void onDestroy() { if (running) stopAll(); super.onDestroy(); }
    @Override public void onRevoke() { stopAll(); }

    private java.io.File libDir() { return new java.io.File(getApplicationInfo().nativeLibraryDir); }

    // ---------- уведомление (обязательно для фоновой службы)
    private void startForegroundNotif(String text) {
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel ch = new NotificationChannel("vpn", "VPN", NotificationManager.IMPORTANCE_LOW);
            ch.setShowBadge(false);
            nm.createNotificationChannel(ch);
        }
        Notification n = buildNotif(text);
        if (Build.VERSION.SDK_INT >= 34)
            startForeground(NOTIF_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE);
        else
            startForeground(NOTIF_ID, n);
    }

    private Notification buildNotif(String text) {
        PendingIntent pi = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class),
                PendingIntent.FLAG_UPDATE_CURRENT | (Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0));
        Notification.Builder nb = (Build.VERSION.SDK_INT >= 26)
                ? new Notification.Builder(this, "vpn") : new Notification.Builder(this);
        return nb.setContentTitle("VPN")
                .setContentText(text)
                .setSmallIcon(R.drawable.ic_stat)
                .setContentIntent(pi)
                .setOngoing(true)
                .build();
    }

    private void updateNotif(String stateJson) {
        String text = "Подключаюсь…";
        try {
            org.json.JSONObject o = new org.json.JSONObject(stateJson);
            String st = o.optString("state");
            if ("on".equals(st)) text = "Подключено · " + o.optString("detail", "");
            else if ("connecting".equals(st)) text = o.optString("detail", "Подключаюсь…");
            else return;
        } catch (Exception e) { return; }
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (nm != null) nm.notify(NOTIF_ID, buildNotif(text));
    }
}
