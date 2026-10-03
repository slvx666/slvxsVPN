package app.vpn;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.File;
import java.io.InputStreamReader;
import java.nio.charset.StandardCharsets;

/** Запуск встроенного ядра (libvpncore.so из nativeLibraryDir). */
public final class Core {
    static File bin(File libDir) { return new File(libDir, "libvpncore.so"); }

    /** Синхронно: скачать и проверить подписку. Возвращает {"ok":true,"tariff":..} или {"error":..}. */
    static JSONObject fetch(File dataDir, File libDir, String url) {
        try {
            Process p = new ProcessBuilder(bin(libDir).getAbsolutePath(), "fetch", dataDir.getAbsolutePath(), url)
                    .redirectErrorStream(false).start();
            String out = "";
            try (BufferedReader r = new BufferedReader(new InputStreamReader(p.getInputStream(), StandardCharsets.UTF_8))) {
                String line;
                while ((line = r.readLine()) != null) if (line.startsWith("{")) out = line;
            }
            p.waitFor();
            if (out.isEmpty()) return new JSONObject().put("error", "Нет ответа");
            return new JSONObject(out);
        } catch (Exception e) {
            try { return new JSONObject().put("error", "Не удалось проверить подписку"); }
            catch (Exception e2) { return new JSONObject(); }
        }
    }
}
