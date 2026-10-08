package app.vpn;

import android.content.Context;
import android.content.SharedPreferences;

public final class Prefs {
    private static SharedPreferences p(Context c) { return c.getSharedPreferences("vpn", Context.MODE_PRIVATE); }
    public static String tariff(Context c) { return p(c).getString("tariff", ""); }
    public static void setTariff(Context c, String t) { p(c).edit().putString("tariff", t).apply(); }
    public static boolean wantOn(Context c) { return p(c).getBoolean("wantOn", false); }
    public static void setWantOn(Context c, boolean v) { p(c).edit().putBoolean("wantOn", v).apply(); }

    // журнал в папке пользователя (LogSink)
    static String logTree(Context c) { return p(c).getString("logTree", ""); }
    static void setLogTree(Context c, String v) { p(c).edit().putString("logTree", v).putString("logDoc", "").putLong("logOff", 0).apply(); }
    static String logDoc(Context c) { return p(c).getString("logDoc", ""); }
    static void setLogDoc(Context c, String v) { p(c).edit().putString("logDoc", v).apply(); }
    static long logOffset(Context c) { return p(c).getLong("logOff", 0); }
    static void setLogOffset(Context c, long v) { p(c).edit().putLong("logOff", v).apply(); }
    static void clearLog(Context c) { p(c).edit().remove("logTree").remove("logDoc").remove("logOff").apply(); }
}
