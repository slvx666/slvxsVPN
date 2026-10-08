package app.vpn;

import android.app.Activity;
import android.content.ClipboardManager;
import android.content.ClipData;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.VpnService;
import android.os.Build;
import android.os.Bundle;
import android.view.View;
import android.view.ViewGroup;
import android.view.Gravity;
import android.widget.TextView;
import android.widget.ScrollView;
import android.webkit.ConsoleMessage;
import android.webkit.JavascriptInterface;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.webkit.WebSettings;
import android.widget.Toast;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.InputStream;
import java.io.PrintWriter;
import java.io.StringWriter;
import java.nio.charset.StandardCharsets;

/** Единственный экран: WebView с интерфейсом (assets/index.html). Вся логика — во встроенном ядре и службе. */
public class MainActivity extends Activity {
    private WebView web;
    private static final int REQ_VPN = 1, REQ_NOTIF = 2, REQ_LOG = 3;

    @Override protected void onCreate(Bundle b) {
        super.onCreate(b);
        // любая ошибка при старте — на экран (иначе виден только серый WebView без подсказки)
        try {
            buildUi();
            checkAutoConnect(getIntent());
        } catch (Throwable t) {
            showFatal(t);
        }
    }

    @Override protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        checkAutoConnect(intent);
    }

    private void checkAutoConnect(Intent intent) {
        if (intent != null && intent.getBooleanExtra("connect", false)) {
            startVpn();
        }
    }

    private void buildUi() throws Exception {
        getWindow().setStatusBarColor(Color.parseColor("#111111"));
        getWindow().setNavigationBarColor(Color.parseColor("#111111"));
        getWindow().getDecorView().setBackgroundColor(Color.parseColor("#111111"));

        WebView.setWebContentsDebuggingEnabled(true);
        web = new WebView(this);
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setMediaPlaybackRequiresUserGesture(false);
        try { s.setAllowFileAccess(true); } catch (Exception ignore) {}
        web.setBackgroundColor(Color.parseColor("#111111"));
        web.setOverScrollMode(View.OVER_SCROLL_NEVER);
        web.addJavascriptInterface(new Bridge(), "Android");
        // ошибки страницы и консоли — на экран, чтобы было видно причину
        web.setWebChromeClient(new WebChromeClient() {
            @Override public boolean onConsoleMessage(ConsoleMessage m) {
                if (m.messageLevel() == ConsoleMessage.MessageLevel.ERROR)
                    toast("JS: " + m.message());
                return true;
            }
        });
        web.setWebViewClient(new WebViewClient() {
            @Override public void onPageFinished(WebView v, String url) { pushHydrate(); }
            @Override public void onReceivedError(WebView v, WebResourceRequest req, WebResourceError err) {
                if (req != null && req.isForMainFrame()) toast("Загрузка: " + err.getDescription());
            }
        });
        setContentView(web);

        // читаем страницу из ресурсов сами и подаём строкой — надёжнее, чем file:// на части устройств
        String html = readAsset("index.html");
        web.loadDataWithBaseURL("https://appassets.local/", html, "text/html", "utf-8", null);

        Bus.setListener(json -> runOnUiThread(() -> {
            if (web != null) web.evaluateJavascript("window.__state && window.__state(" + json + ")", null);
        }));

        if (Build.VERSION.SDK_INT >= 33 &&
                checkSelfPermission("android.permission.POST_NOTIFICATIONS") != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{"android.permission.POST_NOTIFICATIONS"}, REQ_NOTIF);
        }
    }

    private String readAsset(String name) throws Exception {
        try (InputStream in = getAssets().open(name)) {
            ByteArrayOutputStream bo = new ByteArrayOutputStream();
            byte[] buf = new byte[8192];
            int n;
            while ((n = in.read(buf)) > 0) bo.write(buf, 0, n);
            return new String(bo.toByteArray(), StandardCharsets.UTF_8);
        }
    }

    private void toast(String m) {
        runOnUiThread(() -> Toast.makeText(this, m, Toast.LENGTH_LONG).show());
    }

    private void showFatal(Throwable t) {
        StringWriter sw = new StringWriter();
        t.printStackTrace(new PrintWriter(sw));
        TextView tv = new TextView(this);
        tv.setText("Не удалось открыть приложение:\n\n" + sw);
        tv.setTextColor(Color.WHITE);
        tv.setBackgroundColor(Color.parseColor("#111111"));
        tv.setPadding(40, 80, 40, 40);
        tv.setTextIsSelectable(true);
        ScrollView sv = new ScrollView(this);
        sv.addView(tv, new ViewGroup.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));
        setContentView(sv);
    }

    @Override protected void onResume() {
        super.onResume();
        if (web != null) web.evaluateJavascript("window.__state && window.__state(" + Bus.last() + ")", null);
    }

    /** Крестик/кнопка «назад»: сворачиваем в фон, служба и VPN продолжают работать. */
    @Override public void onBackPressed() { moveTaskToBack(true); }

    private File dataDir() { return getFilesDir(); }
    private File libDir() { return new File(getApplicationInfo().nativeLibraryDir); }
    private boolean hasProfile() { return new File(dataDir(), "profile.json").exists(); }

    /** Мост из JS: одно сообщение {id, method, arg}; ответ — window.__resolve(id, value). */
    private class Bridge {
        @JavascriptInterface public void post(String msg) {
            final int[] id = {0};
            final String[] method = {""}, arg = {""};
            try {
                JSONObject o = new JSONObject(msg);
                id[0] = o.optInt("id");
                method[0] = o.optString("method");
                arg[0] = o.optString("arg");
            } catch (Exception e) { return; }
            new Thread(() -> handle(id[0], method[0], arg[0])).start();
        }
    }

    private void resolve(int id, String jsonValue) {
        runOnUiThread(() -> web.evaluateJavascript("window.__resolve(" + id + "," + jsonValue + ")", null));
    }

    private void pushHydrate() {
        try {
            JSONObject o = new JSONObject();
            o.put("sub", hasProfile());
            o.put("tariff", Prefs.tariff(this));
            o.put("logDir", LogSink.dirName(this));
            o.put("state", new JSONObject(Bus.last()));
            final String js = "window.__hydrate && window.__hydrate(" + o + ")";
            runOnUiThread(() -> web.evaluateJavascript(js, null));
        } catch (Exception ignore) {}
    }

    private void handle(int id, String method, String arg) {
        try {
            switch (method) {
                case "init": {
                    JSONObject o = new JSONObject();
                    o.put("sub", hasProfile());
                    o.put("tariff", Prefs.tariff(this));
                    o.put("logDir", LogSink.dirName(this));
                    o.put("state", new JSONObject(Bus.last()));
                    resolve(id, o.toString());
                    break;
                }
                case "paste": {
                    resolve(id, JSONObject.quote(clipboard()));
                    break;
                }
                case "addSub": {
                    JSONObject r = Core.fetch(dataDir(), libDir(), arg);
                    if (r.optBoolean("ok")) Prefs.setTariff(this, r.optString("tariff"));
                    resolve(id, r.toString());
                    break;
                }
                case "logDir": {
                    // системный выбор папки: доступ сохраняется после перезапуска; ответ — через window.__logdir
                    runOnUiThread(() -> {
                        Intent pick = new Intent(Intent.ACTION_OPEN_DOCUMENT_TREE);
                        pick.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION
                                | Intent.FLAG_GRANT_PERSISTABLE_URI_PERMISSION | Intent.FLAG_GRANT_PREFIX_URI_PERMISSION);
                        try { startActivityForResult(pick, REQ_LOG); }
                        catch (Exception e) { toast("На этом телефоне нет выбора папки"); }
                    });
                    resolve(id, "{}");
                    break;
                }
                case "logOff": {
                    Prefs.clearLog(this);
                    resolve(id, "{}");
                    break;
                }
                case "toggle": {
                    if ("on".equals(arg)) startVpn(); else stopVpn();
                    resolve(id, "{}");
                    break;
                }
                default:
                    resolve(id, "{}");
            }
        } catch (Exception e) {
            resolve(id, "{\"error\":\"Ошибка\"}");
        }
    }

    private String clipboard() {
        try {
            ClipboardManager cm = (ClipboardManager) getSystemService(Context.CLIPBOARD_SERVICE);
            ClipData c = cm.getPrimaryClip();
            if (c != null && c.getItemCount() > 0) {
                CharSequence t = c.getItemAt(0).coerceToText(this);
                return t == null ? "" : t.toString();
            }
        } catch (Exception ignore) {}
        return "";
    }

    private void startVpn() {
        Intent prep = VpnService.prepare(this);
        if (prep != null) startActivityForResult(prep, REQ_VPN);
        else launchService();
    }

    private void launchService() {
        Intent i = new Intent(this, VpnServiceImpl.class).setAction(VpnServiceImpl.ACTION_START);
        if (Build.VERSION.SDK_INT >= 26) startForegroundService(i); else startService(i);
    }

    private void stopVpn() {
        startService(new Intent(this, VpnServiceImpl.class).setAction(VpnServiceImpl.ACTION_STOP));
    }

    @Override protected void onActivityResult(int req, int res, Intent data) {
        super.onActivityResult(req, res, data);
        if (req == REQ_LOG) {
            if (res == RESULT_OK && data != null && data.getData() != null) {
                final android.net.Uri u = data.getData();
                try {
                    getContentResolver().takePersistableUriPermission(u,
                            Intent.FLAG_GRANT_READ_URI_PERMISSION | Intent.FLAG_GRANT_WRITE_URI_PERMISSION);
                } catch (Exception ignore) {}
                Prefs.setLogTree(this, u.toString());
                new Thread(() -> {
                    Trouble.log(this, "журнал: выбрана папка для сохранения");
                    LogSink.sync(this);
                    final String name = LogSink.dirName(this);
                    runOnUiThread(() -> web.evaluateJavascript("window.__logdir && window.__logdir(" + JSONObject.quote(name) + ")", null));
                }).start();
            }
            return;
        }
        if (req == REQ_VPN) {
            if (res == RESULT_OK) launchService();
            else Bus.post("{\"state\":\"off\",\"error\":\"Нужно разрешить подключение VPN\"}");
        }
    }
}
