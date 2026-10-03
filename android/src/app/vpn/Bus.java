package app.vpn;

import java.util.function.Consumer;

/** Мост между службой VPN (фон) и экраном: служба шлёт JSON-состояние, экран его показывает. */
public final class Bus {
    private static volatile String last = "{\"state\":\"off\"}";
    private static volatile Consumer<String> listener;

    public static void post(String stateJson) {
        last = stateJson;
        Consumer<String> l = listener;
        if (l != null) l.accept(stateJson);
    }

    public static String last() { return last; }

    public static void setListener(Consumer<String> l) { listener = l; }
}
