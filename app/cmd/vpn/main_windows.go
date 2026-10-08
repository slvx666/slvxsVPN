//go:build windows

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"vpnapp/cmd/vpn/wfp"
	"vpnapp/core"
	"vpnapp/ui"
)

//go:embed wintun.dll
var wintunDLL []byte

var (
	app     *App
	logFile *os.File
)

func logf(format string, a ...any) {
	if logFile != nil {
		fmt.Fprintf(logFile, time.Now().Format("15:04:05 ")+format+"\n", a...)
	}
}

// App связывает окно (WebView2), ядро Xray, встроенный zapret, игровой режим и настройки сети.
type App struct {
	dataDir string
	win     *window

	mu          sync.Mutex
	engine      *core.Engine
	zap         *zapret
	gm          *gameMode
	watcher     *netWatcher
	persist     *persist
	profile     *core.Profile
	phys        *physIface
	tariff      string
	st          core.State
	stoppedSvcs []string
	tuned       map[string]bool
	busy        bool
	userExit    bool
	resuming    bool
}

func main() {
	// режим сторожа: следит за основным процессом (см. watch_windows.go)
	for i, a := range os.Args {
		if a == "--watch" && i+1 < len(os.Args) {
			n := 0
			for _, c := range os.Args[i+1] {
				n = n*10 + int(c-'0')
			}
			watchMode(uint32(n))
			return
		}
	}
	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "VPN")
	if dataDir == "VPN" {
		dataDir = filepath.Join(os.TempDir(), "VPN")
	}
	bridged := false
	// один экземпляр: если уже запущен — показать его окно и выйти
	if h := findExisting(); h != 0 {
		// VPN был включён: пока старая версия закрывается, а мы подключаемся, интернет мимо туннеля не идёт
		// (наши фильтры WFP ставятся ДО закрытия старой и снимаются только заменой на фильтры нового туннеля)
		if loadPersist(dataDir).WantOn {
			bridged = bridgeKillSwitch()
		}
		// запущен другой файл (обычно старая версия после обновления) — закрываем его и стартуем сами;
		// тот же файл — просто показываем уже открытое окно
		if !replaceOther(h) {
			wfp.DisableFirewall()
			postShow(h)
			return
		}
	}
	// VPN был включён (запуск при входе в Windows или после аварии): блокировка с первой миллисекунды,
	// не дожидаясь подключения; её заменит защита, которую поставит подключение
	if !bridged && loadPersist(dataDir).WantOn {
		bridged = bridgeKillSwitch()
	}
	os.MkdirAll(dataDir, 0o755)
	logFile, _ = os.OpenFile(filepath.Join(dataDir, "app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if st, _ := logFile.Stat(); st != nil && st.Size() > 4<<20 {
		logFile.Truncate(0)
	}
	logf("=== запуск VPN ===")
	if !hasArg("--watched") { // перезапущенный сторожем уже под его наблюдением
		spawnWatcher()
	}
	if bridged {
		logf("killswitch: держу блокировку на время смены версии")
	}

	// wintun.dll рядом с ядром: распакуем в данные и загрузим по полному пути (Xray найдёт уже загруженную)
	if err := extractAndLoadWintun(dataDir); err != nil {
		logf("wintun: %v", err)
	}

	app = &App{dataDir: dataDir, persist: loadPersist(dataDir), tuned: map[string]bool{}, st: core.State{State: "off"}}
	// прошлый запуск завершился во время игры (сбой/выключение) — вернуть Wi-Fi автонастройку
	if app.persist.ScanOffIface != "" {
		_ = setWlanScan(app.persist.ScanOffIface, true)
		app.persist.ScanOffIface = ""
		app.persist.save(dataDir)
	}

	showAtStart := !hasArg("--tray")
	win, err := newWindow("VPN", showAtStart, app.onMsg)
	if err != nil {
		messageBox("Не удалось создать окно приложения.", false)
		return
	}
	app.win = win
	win.onClose = func() bool { win.hide(); return true } // крестик — сворачивание в трей, VPN продолжает работать
	win.onTray = app.onTray

	if !win.embed(filepath.Join(dataDir, "webview"), ui.HTML, jsErrHook) {
		if messageBox("Для работы нужен компонент Microsoft Edge WebView2.\n\nОткрыть страницу загрузки? (после установки запустите VPN снова)", true) {
			openURL("https://developer.microsoft.com/microsoft-edge/webview2/")
		}
		return
	}
	win.traySet(false, "VPN — не подключено")

	// профиль (подписка)
	if p, err := core.LoadProfile(dataDir); err == nil {
		app.profile = p
		app.tariff = p.Tariff
	}
	if showAtStart {
		win.show()
	}
	// автозапуск: если в прошлый раз были подключены — поднимаемся сами
	if app.persist.WantOn && app.profile != nil {
		go app.connect()
	} else {
		wfp.DisableFirewall()
	}

	win.run() // цикл сообщений до выхода
	// «Выход» из трея — пользователь выключает VPN. Иначе (обновление закрыло старую версию, выключение
	// или перезагрузка Windows) — только освобождаем систему: при следующем запуске VPN поднимется сам.
	if app.userExit {
		app.disconnect()
	} else {
		app.teardown()
	}
	logf("=== выход ===")
}

// ошибки скрипта интерфейса — в app.log (иначе при чёрном экране нечего смотреть)
const jsErrHook = `window.addEventListener('error',function(e){try{window.chrome.webview.postMessage(JSON.stringify({id:0,method:'jserr',arg:String(e.message)+' @'+e.lineno+':'+e.colno}))}catch(_){}});`

func hasArg(a string) bool {
	for _, v := range os.Args[1:] {
		if strings.EqualFold(v, a) {
			return true
		}
	}
	return false
}

// ---------- wintun

func extractAndLoadWintun(dataDir string) error {
	path := filepath.Join(dataDir, "wintun.dll")
	need := true
	if b, err := os.ReadFile(path); err == nil && len(b) == len(wintunDLL) {
		need = false
	}
	if need {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, wintunDLL, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	// загрузим по полному пути — дальнейший LoadLibraryEx("wintun.dll") вернёт уже загруженный модуль
	if _, err := windows.LoadLibrary(path); err != nil {
		return err
	}
	return nil
}

// ---------- мост UI

func (a *App) onMsg(msg string) {
	var o struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
		Arg    string `json:"arg"`
	}
	if json.Unmarshal([]byte(msg), &o) != nil {
		return
	}
	switch o.Method {
	case "jserr":
		logf("ui: ошибка JS: %s", o.Arg)
		return
	case "init":
		logf("ui: интерфейс запущен")
	}
	go a.handle(o.ID, o.Method, o.Arg)
}

func (a *App) reply(id int, jsonValue string) {
	a.win.eval(fmt.Sprintf("window.__resolve(%d,%s)", id, jsonValue))
}

func (a *App) handle(id int, method, arg string) {
	switch method {
	case "init":
		a.mu.Lock()
		sub := a.profile != nil
		tariff := a.tariff
		st := a.st
		a.mu.Unlock()
		stb, _ := json.Marshal(st)
		a.reply(id, fmt.Sprintf(`{"sub":%v,"tariff":%s,"state":%s}`, sub, jsonStr(tariff), string(stb)))
	case "paste":
		a.reply(id, jsonStr(clipboardText()))
	case "addSub":
		a.reply(id, a.addSub(arg))
	case "toggle":
		if arg == "on" {
			go a.connect()
		} else {
			go a.disconnect()
		}
		a.reply(id, "{}")
	default:
		a.reply(id, "{}")
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func (a *App) addSub(raw string) string {
	u, err := core.NormalizeSubURL(raw)
	if err != nil {
		return `{"error":` + jsonStr(err.Error()) + `}`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	p, err := core.FetchProfile(ctx, u, "windows")
	if err != nil {
		logf("addSub: %v", core.Detail(err))
		return `{"error":` + jsonStr(err.Error()) + `}`
	}
	if err := core.SaveProfile(a.dataDir, p); err != nil {
		return `{"error":"Не удалось сохранить подписку"}`
	}
	a.mu.Lock()
	a.profile = p
	a.tariff = p.Tariff
	a.mu.Unlock()
	// гео и zapret — в фоне (не задерживаем ответ)
	go a.ensureAssets(p)
	return `{"ok":true,"tariff":` + jsonStr(p.Tariff) + `}`
}

func (a *App) ensureAssets(p *core.Profile) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if ref, ok := p.Files[n]; ok {
			core.EnsureFile(ctx, ref, filepath.Join(a.dataDir, n))
		}
	}
}

// ---------- трей

func (a *App) connectedOrConnecting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st.State == "on" || a.st.State == "connecting"
}

func (a *App) updateTray() {
	a.mu.Lock()
	st := a.st
	a.mu.Unlock()
	tip := "VPN — не подключено"
	on := false
	switch st.State {
	case "on":
		on = true
		tip = "VPN — подключено"
		if st.Detail != "" {
			tip = "VPN — " + st.Detail
		}
	case "connecting":
		tip = "VPN — подключаюсь…"
	}
	if a.win != nil {
		a.win.dispatch(func() { a.win.traySet(on, tip) })
	}
}

func (a *App) onTray(cmd int) {
	switch cmd {
	case idOpen:
		a.win.show()
	case idToggle:
		if a.connectedOrConnecting() {
			go a.disconnect()
		} else {
			go a.connect()
		}
	case idExit:
		a.userExit = true
		a.win.quit()
	}
}

func (a *App) onState(s core.State) {
	a.mu.Lock()
	a.st = s
	a.mu.Unlock()
	b, _ := json.Marshal(s)
	if a.win != nil {
		a.win.eval("window.__state && window.__state(" + string(b) + ")")
	}
	a.updateTray()
}

// ---------- подключение

func (a *App) connect() {
	a.mu.Lock()
	if a.busy || (a.engine != nil && a.engine.Running()) {
		a.mu.Unlock()
		return
	}
	a.busy = true
	p := a.profile
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.busy = false; a.mu.Unlock() }()

	if p == nil {
		wfp.DisableFirewall()
		a.onState(core.State{State: "off", Detail: "Сначала добавьте подписку"})
		return
	}
	a.persist.WantOn = true
	a.persist.save(a.dataDir)
	setAutostart(true)
	t0 := time.Now()
	logf("подключение: старт")
	a.onState(core.State{State: "connecting", Detail: "Подбираю лучший путь"})

	ctx := context.Background()
	logf("подключение: гео-базы (+%v)", time.Since(t0).Round(time.Millisecond))
	a.ensureAssets(p)
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(a.dataDir, n)); err != nil {
			wfp.DisableFirewall()
			a.onState(core.State{State: "off", Detail: "Нет базы " + n + " — включите интернет"})
			return
		}
	}

	logf("подключение: адаптер (+%v)", time.Since(t0).Round(time.Millisecond))
	// адаптер может быть ещё недоступен (выход из сна, старая версия только что освободила сеть) — ждём
	phys, err := defaultIface()
	for i := 0; err != nil && i < 15; i++ {
		time.Sleep(time.Second)
		phys, err = defaultIface()
	}
	if err != nil {
		logf("подключение: %v", err)
		wfp.DisableFirewall()
		a.onState(core.State{State: "off", Detail: "Нет подключения к интернету"})
		return
	}
	a.mu.Lock()
	a.phys = phys
	a.mu.Unlock()
	bindDialer(phys.Index)

	// свежая подписка (маршруты, сервисы, сервер) — коротко; не ответила — работаем на сохранённой
	if p.SubURL != "" {
		fctx, fcancel := context.WithTimeout(ctx, 6*time.Second)
		if np, err := core.FetchProfile(fctx, p.SubURL, "windows"); err == nil {
			if core.SaveProfile(a.dataDir, np) == nil {
				p = np
				a.mu.Lock()
				a.profile, a.tariff = np, np.Tariff
				a.mu.Unlock()
				a.ensureAssets(p)
			}
		} else {
			logf("подписка: %v", core.Detail(err))
		}
		fcancel()
	}

	// встроенный обход DPI (zapret) — качается/распаковывается из подписки
	var bp core.Bypass
	logf("подключение: zapret (+%v)", time.Since(t0).Round(time.Millisecond))
	if z, err := prepareZapret(ctx, a.dataDir, p.Files["zapret-win.zip"], p.Server, logf); err == nil {
		z.setIface(phys.Index)
		a.zap = z

		bp = z
	} else {
		logf("zapret недоступен: %v", err)
	}

	// конфликтующие службы обхода — на паузу
	logf("подключение: службы (+%v)", time.Since(t0).Round(time.Millisecond))
	a.stoppedSvcs = stopConflictingServices()

	// Wi-Fi: настройки питания (разово) и игровой режим (отключение сканирования во время игр)
	if phys.WiFi {
		if !a.tuned[phys.Name] {
			a.tuned[phys.Name] = true
			go tuneWiFi(phys.Name)
		}
		a.gm = startGameMode(phys.Name, logf, a.onGameFlip)
	}

	logf("подключение: DNS (+%v)", time.Since(t0).Round(time.Millisecond))
	setSmartDNS(true, a.persist)
	if phys.WiFi {
		go setWlanScan(phys.Name, true) // страховка: автонастройка Wi-Fi должна быть включена (раньше её выключал игровой режим)
	}

	bindName := goIfaceName(phys.Index)
	// диагностика: файл «debug» в папке данных — журнал соединений Xray (access.log): кто куда и через какой выход
	accessLog := ""
	if _, err := os.Stat(filepath.Join(a.dataDir, "debug")); err == nil {
		accessLog = filepath.Join(a.dataDir, "access.log")
		if st, err := os.Stat(accessLog); err == nil && st.Size() > 16<<20 {
			os.Remove(accessLog)
		}
		logf("диагностика: журнал соединений включён (access.log)")
		// проверка выхода из сна без сна: файл «resume-test» в папке данных = имитация пробуждения
		go func() {
			for i := 0; i < 5 || a.Running(); i++ { // первые секунды ядро ещё не создано
				if _, err := os.Stat(filepath.Join(a.dataDir, "resume-test")); err == nil {
					os.Remove(filepath.Join(a.dataDir, "resume-test"))
					logf("диагностика: имитация выхода из сна")
					a.onResume()
				}
				time.Sleep(2 * time.Second)
			}
		}()
	}
	eng := core.NewEngine(core.Options{
		DataDir: a.dataDir, AssetDir: a.dataDir, TunName: tunName, BindIface: bindName,
		Platform: "windows", LogLevel: "warning", ErrorLog: filepath.Join(a.dataDir, "xray.log"), AccessLog: accessLog,
		Bypass: bp, Logf: logf,
		NetKey: func() string {
			a.mu.Lock()
			ph := a.phys
			a.mu.Unlock()
			return netKey(ph)
		},
		OnState: a.onState,
		AfterStart: func() error {
			if err := configureTun(a.currentPhys(), p.Server); err != nil {
				return err
			}
			enableKillSwitch()
			return nil
		},
	})
	a.mu.Lock()
	a.engine = eng
	a.mu.Unlock()

	logf("подключение: ядро (+%v)", time.Since(t0).Round(time.Millisecond))
	if err := eng.Start(p); err != nil {
		logf("ошибка старта ядра: %v", err)
		a.onState(core.State{State: "off", Detail: "Не удалось запустить VPN"})
		a.teardown()
		return
	}
	logf("подключение: готово (+%v)", time.Since(t0).Round(time.Millisecond))
	a.watcher = watchNetwork(a.onNetChange)
}

// Running — ядро запущено (для фоновых диагностических циклов).
func (a *App) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.engine != nil && a.engine.Running()
}

func (a *App) currentPhys() *physIface {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phys
}

// смена сети (Wi-Fi <-> кабель, другой роутер): если сменился адаптер — перепривязываем ядро к нему.
func (a *App) onNetChange() {
	a.mu.Lock()
	eng := a.engine
	old := a.phys
	a.mu.Unlock()
	if eng == nil {
		return
	}
	// При выходе из спящего режима Wi-Fi/адаптеру нужно несколько секунд на получение IP по DHCP
	var phys *physIface
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		phys, err = defaultIface()
		if err == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		logf("сеть недоступна после выхода из сна/смены: %v", err)
		return
	}
	if old != nil && phys.Index == old.Index && phys.Gateway == old.Gateway {
		logf("сеть подтверждена (адаптер %s)", phys.Name)
		// соединения Hysteria2 после обрыва связи мёртвые (QUIC молчит до таймаута простоя) — закрыть сразу
		eng.NetworkChanged()
		return
	}
	logf("сменился адаптер или шлюз: %s -> %s", ifaceName(old), phys.Name)
	a.mu.Lock()
	a.phys = phys
	a.mu.Unlock()
	bindDialer(phys.Index)
	if a.zap != nil {
		a.zap.setIface(phys.Index)
	}
	if a.gm != nil {
		a.gm.Stop()
		a.gm = nil
		if phys.WiFi {
			a.gm = startGameMode(phys.Name, logf, a.onGameFlip)
		}
	}
	if err := eng.Restart(goIfaceName(phys.Index)); err != nil {
		logf("restart: %v", err)
	}
}

// enableKillSwitch — защита от утечек на WFP (динамическая сессия): весь трафик — только через туннель,
// кроме самого приложения (ядро: прямые российские сайты, обход DPI, соединение с сервером), локальной сети
// и DHCP. DNS — только в туннель. Фильтры исчезают при выходе/падении приложения; при ручном отключении снимаются.
// Заодно удаляет старые правила брандмауэра VPN_KillSwitch_*: они блокировали и прямые соединения самого
// ядра (российские сайты и обход DPI не работали), а после выключения VPN оставляли ПК без интернета.
func enableKillSwitch() {
	// новые фильтры заменяют прежние без промежутка (перезапуск ядра: у туннеля мог смениться LUID)
	luid, err := tunLUID()
	if err != nil {
		logf("killswitch: %v", err)
		return
	}
	if err := wfp.EnableFirewall(uint64(luid), false, []netip.Addr{netip.MustParseAddr(core.TunDNS4)}); err != nil {
		logf("killswitch: %v", err)
		return
	}
	logf("killswitch: включён")
	removeLegacyFirewallRules()
}

// bridgeKillSwitch — блокировка на время смены версии: разрешены только мы, ЛВС и текущий туннель старой версии.
func bridgeKillSwitch() bool {
	var luid uint64
	if l, err := tunLUID(); err == nil {
		luid = uint64(l)
	}
	return wfp.EnableFirewall(luid, false, []netip.Addr{netip.MustParseAddr(core.TunDNS4)}) == nil
}

var legacyRules = []string{"VPN_KillSwitch_Block_Physical_IPv4", "VPN_KillSwitch_Block_Wireless_IPv6",
	"VPN_KillSwitch_Block_Wired_IPv6", "VPN_KillSwitch_Block_Physical_DNS_UDP", "VPN_KillSwitch_Block_Physical_DNS_TCP"}

func removeLegacyFirewallRules() {
	for _, n := range legacyRules {
		cmd := exec.Command("netsh", "advfirewall", "firewall", "delete", "rule", "name="+n)
		cmd.SysProcAttr = hiddenProc()
		if cmd.Run() == nil {
			logf("брандмауэр: удалено старое правило %s", n)
		}
	}
}

// onGameFlip запоминает, что автонастройка Wi-Fi выключена нами (вернуть её после сбоя).
func (a *App) onGameFlip(off bool, iface string) {
	if off {
		a.persist.ScanOffIface = iface
	} else {
		a.persist.ScanOffIface = ""
	}
	a.persist.save(a.dataDir)
}

// onResume — ПК проснулся (сон/гибернация). Все соединения за время сна мертвы, адаптер мог получить новый адрес:
// ждём сеть и пересоздаём ядро (Restart закрывает и туннель, и клиентов Hysteria2). Защита WFP не снимается.
func (a *App) onResume() {
	a.mu.Lock()
	if a.resuming || a.engine == nil {
		a.mu.Unlock()
		return
	}
	a.resuming = true
	eng := a.engine
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.resuming = false; a.mu.Unlock() }()

	logf("выход из сна: жду сеть")
	var phys *physIface
	var err error
	for i := 0; i < 120; i++ {
		if phys, err = defaultIface(); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		logf("выход из сна: сети нет — продолжу, когда появится (%v)", err)
		return // появится маршрут — сработает watchNetwork -> onNetChange
	}
	time.Sleep(2 * time.Second) // DHCP/DNS адаптера доустанавливаются
	a.mu.Lock()
	a.phys = phys
	a.mu.Unlock()
	bindDialer(phys.Index)
	if a.zap != nil {
		a.zap.setIface(phys.Index)
	}
	logf("выход из сна: сеть есть (%s), пересоздаю ядро", phys.Name)
	if err := eng.Restart(goIfaceName(phys.Index)); err != nil {
		logf("выход из сна: restart: %v", err)
		// второй шанс: туннель мог занять не сразу
		time.Sleep(3 * time.Second)
		if err := eng.Restart(goIfaceName(phys.Index)); err != nil {
			logf("выход из сна: restart (повтор): %v", err)
		}
	}
}

func ifaceName(p *physIface) string {
	if p == nil {
		return "?"
	}
	return p.Name
}

func (a *App) disconnect() {
	a.persist.WantOn = false
	a.persist.save(a.dataDir)
	setAutostart(false)
	a.teardown()
	a.onState(core.State{State: "off"})
}

// teardown останавливает всё и возвращает систему в исходное состояние.
func (a *App) teardown() {
	a.mu.Lock()
	eng := a.engine
	a.engine = nil
	watcher := a.watcher
	a.watcher = nil
	gm := a.gm
	a.gm = nil
	zap := a.zap
	a.zap = nil
	phys := a.phys
	svcs := a.stoppedSvcs
	a.stoppedSvcs = nil
	srv := ""
	if a.profile != nil {
		srv = a.profile.Server
	}
	a.mu.Unlock()

	if watcher != nil {
		watcher.Stop()
	}
	if gm != nil {
		gm.Stop()
	}
	if eng != nil {
		eng.Stop()
	}
	if zap != nil {
		zap.Stop()
	}
	startServices(svcs)
	wfp.DisableFirewall() // ручное отключение — обычный интернет без VPN
	cleanupTun(phys, srv)
	setSmartDNS(false, a.persist)
	core.DirectDialer.Control = nil
}
