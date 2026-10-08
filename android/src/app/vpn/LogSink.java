package app.vpn;

import android.content.ContentResolver;
import android.content.Context;
import android.database.Cursor;
import android.net.Uri;
import android.os.Build;
import android.provider.DocumentsContract;

import java.io.File;
import java.io.FileInputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;

/** Копирование журнала проблем (trouble.log) в папку, которую выбрал пользователь (системный выбор папки, SAF):
 *  файл vpn-log.txt дописывается каждые ~15 с, пока включён VPN, и при выключении. Доступ к папке остаётся
 *  после перезапуска приложения; файл можно открыть/отправить любым файловым менеджером. */
final class LogSink {
    static final String FILE_NAME = "vpn-log.txt";
    private static final long MAX_SIZE = 10L << 20; // файл в папке пользователя не больше 10 МБ

    static Uri tree(Context c) {
        String s = Prefs.logTree(c);
        return s.isEmpty() ? null : Uri.parse(s);
    }

    /** Имя выбранной папки для показа в интерфейсе. */
    static String dirName(Context c) {
        Uri t = tree(c);
        if (t == null) return "";
        try {
            Uri doc = DocumentsContract.buildDocumentUriUsingTree(t, DocumentsContract.getTreeDocumentId(t));
            try (Cursor q = c.getContentResolver().query(doc, new String[]{DocumentsContract.Document.COLUMN_DISPLAY_NAME}, null, null, null)) {
                if (q != null && q.moveToFirst()) return q.getString(0);
            }
        } catch (Exception ignore) {}
        return "папка выбрана";
    }

    /** Дописать новые строки trouble.log в vpn-log.txt. Безопасно вызывать из любого потока. */
    static synchronized void sync(Context c) {
        Uri t = tree(c);
        if (t == null) return;
        try {
            File dir = c.getExternalFilesDir(null);
            if (dir == null) dir = c.getFilesDir();
            File src = new File(dir, "trouble.log");
            long len = src.length();
            long off = Prefs.logOffset(c);
            if (len < off) off = 0; // файл ужат/ротирован — начинаем сначала
            ContentResolver cr = c.getContentResolver();
            Uri doc = docUri(c, cr, t);
            if (doc == null) return;
            if (len <= off) return;
            long n = Math.min(len - off, 1L << 20);
            byte[] buf = new byte[(int) n];
            try (FileInputStream in = new FileInputStream(src)) {
                in.skip(off);
                int r = in.read(buf);
                if (r <= 0) return;
                if (r < buf.length) buf = java.util.Arrays.copyOf(buf, r);
            }
            // не режем посреди строки: пишем до последнего перевода строки
            int end = buf.length;
            if (off + buf.length < len) {
                int nl = lastNewline(buf);
                if (nl > 0) end = nl + 1;
            }
            boolean truncate = size(cr, doc) > MAX_SIZE;
            try (OutputStream o = cr.openOutputStream(doc, truncate ? "wt" : "wa")) {
                if (o == null) return;
                if (truncate) o.write("=== журнал обрезан (достиг 10 МБ) ===\n".getBytes(StandardCharsets.UTF_8));
                o.write(buf, 0, end);
            }
            Prefs.setLogOffset(c, off + end);
        } catch (Exception e) {
            // папку удалили/доступ отозван — молча, повторим при следующей синхронизации
        }
    }

    private static int lastNewline(byte[] b) {
        for (int i = b.length - 1; i >= 0; i--) if (b[i] == '\n') return i;
        return -1;
    }

    private static long size(ContentResolver cr, Uri doc) {
        try (Cursor q = cr.query(doc, new String[]{DocumentsContract.Document.COLUMN_SIZE}, null, null, null)) {
            if (q != null && q.moveToFirst() && !q.isNull(0)) return q.getLong(0);
        } catch (Exception ignore) {}
        return 0;
    }

    private static Uri docUri(Context c, ContentResolver cr, Uri tree) {
        String saved = Prefs.logDoc(c);
        if (!saved.isEmpty()) {
            Uri u = Uri.parse(saved);
            try (Cursor q = cr.query(u, new String[]{DocumentsContract.Document.COLUMN_DISPLAY_NAME}, null, null, null)) {
                if (q != null && q.moveToFirst()) return u;
            } catch (Exception ignore) {}
        }
        try {
            Uri parent = DocumentsContract.buildDocumentUriUsingTree(tree, DocumentsContract.getTreeDocumentId(tree));
            Uri u = DocumentsContract.createDocument(cr, parent, "text/plain", FILE_NAME);
            if (u == null) return null;
            Prefs.setLogDoc(c, u.toString());
            Prefs.setLogOffset(c, 0);
            try (OutputStream o = cr.openOutputStream(u, "wt")) {
                if (o != null) o.write(("=== журнал VPN " + Trouble.ver(c) + " | " + Build.MANUFACTURER + " " + Build.MODEL
                        + " | Android " + Build.VERSION.RELEASE + " ===\n").getBytes(StandardCharsets.UTF_8));
            }
            return u;
        } catch (Exception e) {
            return null;
        }
    }
}
