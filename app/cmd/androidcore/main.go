// androidcore — ядро приложения VPN для Android (исполняемый файл libvpncore.so из nativeLibraryDir).
//
//	libvpncore.so fetch <dataDir> <url>          — скачать подписку, вывести {"ok":true,"tariff":...} или {"error":...}
//	libvpncore.so run <dataDir> <libDir> <sock>  — получить дескриптор TUN через абстрактный unix-сокет <sock>,
//	                                               запустить VPN; состояние — JSON-строки в stdout;
//	                                               команды в stdin: "net" (сменилась сеть), "stop".
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"vpnapp/bdpi"
	"vpnapp/core"
)

var outMu sync.Mutex

func emit(v any) {
	b, _ := json.Marshal(v)
	outMu.Lock()
	os.Stdout.Write(append(b, '\n'))
	outMu.Unlock()
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: fetch|run ...")
		os.Exit(2)
	}
	dataDir := os.Args[2]
	os.MkdirAll(dataDir, 0o755)
	logFile, _ := os.OpenFile(filepath.Join(dataDir, "core.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if st, err := logFile.Stat(); err == nil && st.Size() > 2<<20 {
		logFile.Truncate(0)
	}
	log.SetOutput(logFile)
	switch os.Args[1] {
	case "fetch":
		fetch(dataDir, os.Args[3])
	case "run":
		run(dataDir, os.Args[3], os.Args[4])
	}
}

func fetch(dataDir, raw string) {
	u, err := core.NormalizeSubURL(raw)
	if err != nil {
		emit(map[string]any{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	p, err := core.FetchProfile(ctx, u, "android")
	if err != nil {
		log.Print("fetch: ", core.Detail(err))
		emit(map[string]any{"error": err.Error()})
		return
	}
	if err := core.SaveProfile(dataDir, p); err != nil {
		emit(map[string]any{"error": "Не удалось сохранить подписку"})
		return
	}
	updateFiles(ctx, dataDir, p)
	emit(map[string]any{"ok": true, "tariff": p.Tariff})
}

func updateFiles(ctx context.Context, dataDir string, p *core.Profile) {
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if ref, ok := p.Files[n]; ok {
			if _, err := core.EnsureFile(ctx, ref, filepath.Join(dataDir, n)); err != nil {
				log.Printf("update %s: %v", n, err)
			}
		}
	}
}

func recvFd(name string) (int, error) {
	c, err := net.Dial("unix", "@"+name)
	if err != nil {
		return -1, err
	}
	defer c.Close()
	uc := c.(*net.UnixConn)
	buf := make([]byte, 8)
	oob := make([]byte, unix.CmsgSpace(4*4))
	uc.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, oobn, _, _, err := uc.ReadMsgUnix(buf, oob)
	if err != nil {
		return -1, err
	}
	msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(msgs) == 0 {
		return -1, fmt.Errorf("no fd: %v", err)
	}
	fds, err := unix.ParseUnixRights(&msgs[0])
	if err != nil || len(fds) == 0 {
		return -1, fmt.Errorf("no fd: %v", err)
	}
	return fds[0], nil
}

// ensureGeo проверяет geoip.dat/geosite.dat, докачивает при отсутствии; возвращает имя недостающей.
func ensureGeo(dataDir string, p *core.Profile) string {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		path := filepath.Join(dataDir, n)
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			continue
		}
		if ref, ok := p.Files[n]; ok {
			if _, err := core.EnsureFile(ctx, ref, path); err != nil {
				log.Printf("geo %s: %v", n, err)
			}
		}
		if st, err := os.Stat(path); err != nil || st.Size() == 0 {
			return n
		}
	}
	return ""
}

// watchPanic следит за логом ошибок Xray: при первой панике в TUN показывает причину (VPN продолжает работать).
func watchPanic(path string, e *core.Engine) {
	for i := 0; i < 600; i++ {
		time.Sleep(2 * time.Second)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := string(b)
		idx := strings.Index(s, "HandleConnection panic")
		if idx < 0 {
			continue
		}
		block := s[idx:]
		// причина (значение паники) — первая строка
		reason := block
		if j := strings.IndexByte(reason, '\n'); j >= 0 {
			reason = reason[:j]
		}
		if k := strings.Index(reason, ": "); k >= 0 && strings.Contains(reason, "runtime error") {
			if kk := strings.Index(reason, "runtime error"); kk >= 0 {
				reason = reason[kk:]
			}
		}
		// виновник — первый кадр Xray-кода ПОСЛЕ строки panic(
		culprit := ""
		if pi := strings.Index(block, "\npanic("); pi >= 0 {
			lines := strings.Split(block[pi:], "\n")
			for i, ln := range lines {
				ln = strings.TrimSpace(ln)
				if strings.Contains(ln, "xray-core/") && strings.Contains(ln, "(") {
					culprit = ln
					if i+1 < len(lines) { // следующая строка — файл:строка
						if fl := strings.TrimSpace(lines[i+1]); strings.Contains(fl, ".go:") {
							if sp := strings.LastIndex(fl, "xray-core"); sp >= 0 {
								fl = fl[sp:]
							}
							if sp := strings.IndexByte(fl, ' '); sp >= 0 {
								fl = fl[:sp]
							}
							culprit += " " + fl
						}
					}
					break
				}
			}
		}
		msg := "Сбой UDP: " + reason
		if culprit != "" {
			if len(culprit) > 120 {
				culprit = culprit[:120]
			}
			msg += " @ " + culprit
		}
		if len(msg) > 240 {
			msg = msg[:240]
		}
		st := e.State()
		m := map[string]any{"state": st.State, "detail": st.Detail, "tcp": st.TCP, "udp": st.UDP, "error": msg}
		emit(m)
		log.Print("PANIC: ", s[idx:min(len(s), idx+2500)])
		return
	}
}


// Журнал проблем: trouble.log в папке диагностики (Android/data/app.vpn/files) — только значимое: сводки
// проверок со сбоями/медленными ответами, смены состояния и протоколов, подбор обхода, смена сети, события
// устройства из Java (сигнал, батарея, «дрёма»). Пишется в релизе всегда, ≤ 3 МБ (старое уходит в .1).
var (
	troubleMu   sync.Mutex
	troublePath string
)

// troubleZone — часовой пояс телефона: у Go-процесса на Android нет tzdata, служба передаёт смещение в секундах.
func troubleZone() *time.Location {
	if v, err := strconv.Atoi(os.Getenv("VPN_TZ_OFFSET")); err == nil {
		return time.FixedZone("tz", v)
	}
	return time.UTC
}

func tlog(format string, a ...any) {
	if troublePath == "" {
		return
	}
	troubleMu.Lock()
	defer troubleMu.Unlock()
	if st, err := os.Stat(troublePath); err == nil && st.Size() > 3<<20 {
		os.Remove(troublePath + ".1")
		os.Rename(troublePath, troublePath+".1")
	}
	f, err := os.OpenFile(troublePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s [ядро] %s\n", time.Now().In(troubleZone()).Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

func run(dataDir, libDir, sock string) {
	if d := os.Getenv("VPN_DIAG_DIR"); d != "" {
		os.MkdirAll(d, 0o755)
		troublePath = filepath.Join(d, "trouble.log")
	} else {
		troublePath = filepath.Join(dataDir, "trouble.log")
	}
	tlog("=== запуск ядра %s, сеть %s", core.Version, os.Getenv("VPN_NETKEY"))
	logf := func(f string, a ...any) {
		log.Printf(f, a...)
		// сводка кругов заменяет построчные «probe …»; остальное — в журнал проблем
		if !strings.HasPrefix(f, "probe ") {
			tlog(f, a...)
		}
	}
	p, err := core.LoadProfile(dataDir)
	if err != nil {
		emit(core.State{State: "off", Detail: "Сначала добавьте подписку"})
		os.Exit(1)
	}
	// гео-базы обязаны быть на месте — иначе конфиг Xray не загрузится и ядро упадёт без понятной причины
	if miss := ensureGeo(dataDir, p); miss != "" {
		emit(map[string]any{"state": "off", "error": "Нет базы " + miss + " — включите интернет и попробуйте снова"})
		os.Exit(1)
	}
	tunFd, err := recvFd(sock)
	if err != nil {
		log.Print("recv fd: ", err)
		emit(map[string]any{"state": "off", "error": "Не удалось запустить VPN"})
		os.Exit(1)
	}
	// основное ядро — общее с ПК (Xray: выбор протокола, подбор обхода по сети, маршруты из подписки);
	// sing-box — запасной, только если он есть в сборке и включён файлом «singbox» в данных приложения
	singboxBin := filepath.Join(libDir, "libsingbox.so")
	_, sbFlag := os.Stat(filepath.Join(dataDir, "singbox"))
	if _, err := os.Stat(singboxBin); err == nil && sbFlag == nil {
		logf("starting sing-box core...")
		runner, err := startSingbox(dataDir, libDir, tunFd, p, logf)
		if err != nil {
			log.Print("start singbox: ", err)
			os.Exit(1)
		}

		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		cmds := make(chan string)
		go func() {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				cmds <- sc.Text()
			}
			close(cmds)
		}()
		for {
			select {
			case <-sig:
				runner.stop()
				return
			case c, ok := <-cmds:
				if !ok || c == "stop" {
					runner.stop()
					return
				}
				if strings.HasPrefix(c, "net") {
					runner.onNetworkChange()
				}
			}
		}
	}

	// свежая подписка (маршруты, сервисы) — коротко, до старта; не ответила — работаем на сохранённой
	if p.SubURL != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if np, err := core.FetchProfile(ctx, p.SubURL, "android"); err == nil {
			if core.SaveProfile(dataDir, np) == nil {
				p = np
			}
		} else {
			logf("подписка: %v", core.Detail(err))
		}
		cancel()
	}

	var curFd = -1
	xrayLog := filepath.Join(dataDir, "xray.log")
	os.Remove(xrayLog)
	bp := bdpi.New(filepath.Join(libDir, "libbyedpi.so"), 10801, logf)
	var eng *core.Engine
	e := core.NewEngine(core.Options{
		DataDir: dataDir, AssetDir: dataDir, TunName: "tun0", Platform: "android",
		LogLevel:  logLevel(dataDir, os.Getenv("VPN_DIAG_DIR")),
		ErrorLog: xrayLog, AccessLog: accessLog(dataDir, os.Getenv("VPN_DIAG_DIR")),
		Bypass:   bp, Logf: logf,
		NetKey: func() string { return os.Getenv("VPN_NETKEY") },
		OnState: func(s core.State) { emit(s) },
		Event:   func(m string) { tlog("%s", m) },
		BeforeStart: func() error {
			// каждому запуску ядра — свой дубликат дескриптора (ядро закрывает свой при остановке)
			if curFd >= 0 {
				unix.Close(curFd)
			}
			fd, err := unix.Dup(tunFd)
			if err != nil {
				return err
			}
			curFd = fd
			os.Setenv("xray.tun.fd", strconv.Itoa(fd))
			return nil
		},
	})
	if err := e.Start(p); err != nil {
		log.Print("start: ", err)
		emit(map[string]any{"state": "off", "error": "Не удалось запустить VPN"})
		os.Exit(1)
	}
	eng = e
	go watchPanic(xrayLog, eng) // паника в обработке пакета не роняет VPN — покажем причину один раз
	go diagCopy(dataDir, os.Getenv("VPN_DIAG_DIR"))
	// свежие гео-базы и подписка (тариф, адрес сервера) — в фоне; применятся при следующем подключении
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if np, err := core.FetchProfile(ctx, p.SubURL, "android"); err == nil {
			core.SaveProfile(dataDir, np)
			updateFiles(ctx, dataDir, np)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	cmds := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			cmds <- sc.Text()
		}
		close(cmds) // приложение закрыло поток — выходим
	}()
	for {
		select {
		case <-sig:
			e.Stop()
			return
		case c, ok := <-cmds:
			if !ok || c == "stop" {
				e.Stop()
				return
			}
			if len(c) > 4 && c[:4] == "net " {
				// Android: наш трафик и так вне туннеля (disallowApplication self), поэтому при смене сети
				// НЕ пересоздаём ядро (это рвёт все соединения и мигает "Подключаюсь"). Новая сеть (Wi-Fi <-> LTE):
				// закрываем соединения Hysteria2 старой сети (иначе висели бы до таймаута) и перепроверяем
				// протоколы и обход. Тот же handle (повтор события) — только перепроверка.
				// «net <номер сети Android> <отпечаток сети>»
				parts := strings.Fields(c[4:])
				if len(parts) > 1 {
					os.Setenv("VPN_NETKEY", parts[1])
				}
				if len(parts) > 0 && parts[0] != os.Getenv("VPN_NETHANDLE") {
					prev := os.Getenv("VPN_NETHANDLE")
					os.Setenv("VPN_NETHANDLE", parts[0])
					if prev != "" {
						logf("сеть сменилась: %s -> %s (%s)", prev, parts[0], os.Getenv("VPN_NETKEY"))
						e.NetworkChanged()
						continue
					}
				}
				e.Kick()
			}
		}
	}
}

// Диагностика без root и без отладочной сборки: флаг — файл "debug" в files/ приложения ИЛИ в папке
// VPN_DIAG_DIR (Android/data/app.vpn/files, туда можно положить по adb). Слово debug в нём — ещё и
// подробный лог Xray. Логи ядра копируются в VPN_DIAG_DIR (см. diagCopy).
func debugFlag(dirs ...string) string {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(d, "debug")); err == nil {
			return "on " + string(b)
		}
	}
	return ""
}

func accessLog(dataDir, diagDir string) string {
	if debugFlag(dataDir, diagDir) == "" {
		return ""
	}
	p := filepath.Join(dataDir, "access.log")
	if st, err := os.Stat(p); err == nil && st.Size() > 8<<20 {
		os.Remove(p)
	}
	return p
}

func logLevel(dataDir, diagDir string) string {
	if strings.Contains(debugFlag(dataDir, diagDir), "debug") {
		return "debug"
	}
	return "warning"
}

// diagCopy раз в 15 с кладёт хвосты логов (до 1 МБ) в diagDir — их видно по adb в Android/data/app.vpn/files.
func diagCopy(dataDir, diagDir string) {
	if diagDir == "" {
		return
	}
	os.MkdirAll(diagDir, 0o755)
	for {
		for _, n := range []string{"core.log", "xray.log", "access.log", "cache.json"} {
			b, err := os.ReadFile(filepath.Join(dataDir, n))
			if err != nil {
				continue
			}
			if len(b) > 1<<20 {
				b = b[len(b)-1<<20:]
			}
			_ = os.WriteFile(filepath.Join(diagDir, n), b, 0o644)
		}
		time.Sleep(15 * time.Second)
	}
}
