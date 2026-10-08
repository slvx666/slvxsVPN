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
import android.net.NetworkCapabilities;
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
    static final int MTU = 1280, NOTIF_ID = 1;

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
        Trouble.log(this, "=== VPN включается (версия " + Trouble.ver(this) + ") " + Trouble.snapshot(this));
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
                    .addDnsServer(TUN_DNS4)
                    .addDnsServer("77.88.8.8")
                    .addRoute("0.0.0.0", 0)
                    // IPv6 тоже в туннель: иначе на мобильном интернете с IPv6 часть трафика шла бы мимо VPN
                    .addAddress(TUN_ADDR6, 126)
                    .addRoute("::", 0)
                    .setConfigureIntent(PendingIntent.getActivity(this, 0,
                            new Intent(this, MainActivity.class),
                            PendingIntent.FLAG_UPDATE_CURRENT | (Build.VERSION.SDK_INT >= 23 ? PendingIntent.FLAG_IMMUTABLE : 0)));
            // наш собственный трафик (ядро, подписка, обновления) — мимо туннеля, иначе получится петля
            try { b.addDisallowedApplication(getPackageName()); } catch (Exception ignore) {}
            if (Build.VERSION.SDK_INT >= 29) b.setMetered(false);
            tun = b.establish();
            if (tun == null) { fail("VPN не разрешён"); return; }
            if (Build.VERSION.SDK_INT >= 22) {
                try {
                    ConnectivityManager cm = getSystemService(ConnectivityManager.class);
                    if (cm != null) {
                        Network act = physical(cm);
                        if (act != null) setUnder(new Network[]{act});
                    }
                } catch (Exception ignore) {}
            }

            String name = "vpn_tun_" + System.nanoTime();
            sock = new LocalServerSocket(name);

            ProcessBuilder pb = new ProcessBuilder(Core.bin(libDir()).getAbsolutePath(),
                    "run", getFilesDir().getAbsolutePath(), libDir().getAbsolutePath(), name)
                    .redirectErrorStream(false);
            // папка для диагностики (Android/data/app.vpn/files): ядро копирует туда логи, читается по adb без root
            java.io.File diag = getExternalFilesDir(null);
            if (diag != null) pb.environment().put("VPN_DIAG_DIR", diag.getAbsolutePath());
            pb.environment().put("VPN_TZ_OFFSET", String.valueOf(java.util.TimeZone.getDefault().getOffset(System.currentTimeMillis()) / 1000));
            // текущая сеть: ядро сразу берёт удачный для неё вариант обхода
            try {
                ConnectivityManager cm0 = getSystemService(ConnectivityManager.class);
                Network act = physical(cm0);
                if (act != null) {
                    pb.environment().put("VPN_NETKEY", netKey(cm0, act));
                    pb.environment().put("VPN_NETHANDLE", String.valueOf(act.getNetworkHandle()));
                    netHandle = act.getNetworkHandle();
                }
            } catch (Exception ignore) {}
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
            startSnapshots();
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
            Trouble.log(this, "ЯДРО ОСТАНОВИЛОСЬ: " + reason + " | " + Trouble.snapshot(this));
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

    /** Реальная сеть (не наш VPN): Wi-Fi/кабель предпочтительнее мобильной — как выбирает и сама система. */
    static Network physical(ConnectivityManager cm) {
        Network best = null;
        if (cm == null) return null;
        for (Network n : cm.getAllNetworks()) {
            NetworkCapabilities c = cm.getNetworkCapabilities(n);
            if (c == null || c.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
                    || !c.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)) continue;
            if (c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) || c.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)) return n;
            if (best == null) best = n;
        }
        return best;
    }

    private long netHandle = -1;
    private ConnectivityManager.NetworkCallback defCb;

    /** Диагностика: файл «nounder» в папке журнала отключает setUnderlyingNetworks (VPN следует за сетью по умолчанию). */
    private void setUnder(Network[] n) {
        if (Build.VERSION.SDK_INT < 22) return;
        try {
            java.io.File d = getExternalFilesDir(null);
            if (d != null && new java.io.File(d, "nounder").exists()) return;
            setUnderlyingNetworks(n);
        } catch (Exception ignore) {}
    }
    private final android.os.Handler main = new android.os.Handler(android.os.Looper.getMainLooper());

    /** Сменилась реальная сеть: сообщить системе (нижележащая сеть VPN) и ядру. Свой VPN не считается.
     *  hint — сеть по умолчанию для нашего приложения (оно исключено из VPN, значит это именно физическая сеть). */
    private synchronized void netChanged(Network hint, String why) {
        ConnectivityManager cm = getSystemService(ConnectivityManager.class);
        Network p = null;
        if (hint != null) {
            NetworkCapabilities c = cm.getNetworkCapabilities(hint);
            if (c != null && !c.hasTransport(NetworkCapabilities.TRANSPORT_VPN)
                    && c.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)) p = hint;
        }
        if (p == null) p = physical(cm);
        if (Build.VERSION.SDK_INT >= 22) {
            setUnder(p != null ? new Network[]{p} : null);
        }
        long h = p == null ? 0 : p.getNetworkHandle();
        if (h == netHandle) return;
        netHandle = h;
        Trouble.log(this, "СМЕНА СЕТИ (" + why + ") " + Trouble.snapshot(this));
        if (p != null) toCore("net " + h + " " + netKey(cm, p));
    }

    private void netChangedSoon(String why) {
        netChanged(null, why);
        // подстраховка: система может обновить список сетей с задержкой после потери Wi-Fi
        main.postDelayed(() -> { if (running) netChanged(null, why + "+0.5с"); }, 500);
        main.postDelayed(() -> { if (running) netChanged(null, why + "+3с"); }, 3000);
    }

    private void registerNetCallback() {
        ConnectivityManager cm = getSystemService(ConnectivityManager.class);
        if (cm == null) return;
        netCb = new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network n) { netChangedSoon("доступна"); }
            @Override public void onCapabilitiesChanged(Network n, NetworkCapabilities caps) { netChanged(null, "параметры"); }
            @Override public void onLost(Network n) { Trouble.log(VpnServiceImpl.this, "сеть потеряна " + n.getNetworkHandle()); netChangedSoon("потеряна"); }
        };
        try {
            cm.registerNetworkCallback(new android.net.NetworkRequest.Builder()
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET).build(), netCb);
        } catch (Exception ignore) {}
        // сеть по умолчанию для нашего приложения (исключено из VPN) — то, что система выбрала сейчас
        defCb = new ConnectivityManager.NetworkCallback() {
            @Override public void onAvailable(Network n) { netChanged(n, "по умолчанию"); }
            @Override public void onLost(Network n) { netChangedSoon("по умолчанию потеряна"); }
        };
        try { cm.registerDefaultNetworkCallback(defCb); } catch (Exception ignore) {}
    }

    /** Устойчивый «отпечаток» сети (номер сети Android меняется при каждом переподключении):
     *  Wi-Fi/кабель — шлюз и DNS, мобильная — оператор. Для запоминания удачного обхода DPI. */
    String netKey(ConnectivityManager cm, Network n) {
        StringBuilder b = new StringBuilder();
        try {
            android.net.NetworkCapabilities c = cm.getNetworkCapabilities(n);
            if (c != null && c.hasTransport(android.net.NetworkCapabilities.TRANSPORT_CELLULAR)) {
                b.append("cell:");
                try {
                    android.telephony.TelephonyManager tm = (android.telephony.TelephonyManager)
                            getSystemService(TELEPHONY_SERVICE);
                    if (tm != null) b.append(tm.getNetworkOperator()).append(':').append(tm.getNetworkOperatorName());
                } catch (Exception ignore) {}
            } else {
                b.append(c != null && c.hasTransport(android.net.NetworkCapabilities.TRANSPORT_WIFI) ? "wifi:" : "net:");
                android.net.LinkProperties lp = cm.getLinkProperties(n);
                if (lp != null) {
                    for (android.net.RouteInfo r : lp.getRoutes())
                        if (r.isDefaultRoute() && r.getGateway() != null) b.append(r.getGateway().getHostAddress()).append(',');
                    for (java.net.InetAddress d : lp.getDnsServers()) b.append(d.getHostAddress()).append(',');
                }
            }
        } catch (Exception ignore) {}
        return Integer.toHexString(b.toString().hashCode()) + "-" + b.toString().replaceAll("[^A-Za-z0-9.:,-]", "").replaceAll("^(.{0,40}).*", "$1");
    }

    private void fail(String msg) {
        Bus.post("{\"state\":\"off\",\"error\":\"" + msg + "\"}");
        stopAll();
    }

    private Thread snaps;

    /** Раз в минуту — снимок состояния устройства в журнал; каждые 15 с — копия новых строк в папку пользователя. */
    private void startSnapshots() {
        snaps = new Thread(() -> {
            int tick = 0;
            while (running) {
                try { Thread.sleep(15000); } catch (InterruptedException e) { return; }
                if (!running) return;
                if (++tick % 4 == 0) Trouble.log(this, "снимок " + Trouble.snapshot(this));
                LogSink.sync(this);
            }
        }, "vpn-snap");
        snaps.setDaemon(true);
        snaps.start();
    }

    private void stopAll() {
        if (running) Trouble.log(this, "=== VPN выключается " + Trouble.snapshot(this));
        running = false;
        if (snaps != null) snaps.interrupt();
        new Thread(() -> LogSink.sync(this), "vpn-logflush").start();
        Prefs.setWantOn(this, false);
        if (Build.VERSION.SDK_INT >= 22) {
            setUnder(null);
        }
        ConnectivityManager cm = getSystemService(ConnectivityManager.class);
        if (cm != null && netCb != null) { try { cm.unregisterNetworkCallback(netCb); } catch (Exception ignore) {} netCb = null; }
        if (cm != null && defCb != null) { try { cm.unregisterNetworkCallback(defCb); } catch (Exception ignore) {} defCb = null; }
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

    @Override public void onDestroy() {
        Trouble.log(this, "служба уничтожена (работала=" + running + ")");
        if (running) stopAll();
        super.onDestroy();
    }
    @Override public void onRevoke() { Trouble.log(this, "VPN отозван системой (включился другой VPN?)"); stopAll(); }
    @Override public void onTrimMemory(int level) { Trouble.log(this, "нехватка памяти, уровень " + level); super.onTrimMemory(level); }

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
