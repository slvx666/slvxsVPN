package app.vpn;

import android.content.Context;
import android.content.SharedPreferences;

public final class Prefs {
    private static SharedPreferences p(Context c) { return c.getSharedPreferences("vpn", Context.MODE_PRIVATE); }
    public static String tariff(Context c) { return p(c).getString("tariff", ""); }
    public static void setTariff(Context c, String t) { p(c).edit().putString("tariff", t).apply(); }
    public static boolean wantOn(Context c) { return p(c).getBoolean("wantOn", false); }
    public static void setWantOn(Context c, boolean v) { p(c).edit().putBoolean("wantOn", v).apply(); }
}
