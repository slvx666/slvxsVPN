package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	xcore "github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
	"github.com/xtls/xray-core/transport/internet/hysteria"
)

// Bypass — встроенный обход DPI для сервисов mode=bypass (ПК: zapret/winws, Android: ByeDPI).
type Bypass interface {
	Outbounds() []obj                                 // дополнительные выходы Xray (например, SOCKS до ByeDPI)
	Strategies() []string                             // варианты обхода по порядку; 0 — «без обхода»
	Apply(ctx context.Context, i int) (string, error) // включить вариант i, вернуть тег выхода; i<0 — выключить
	Stop()
}

type Options struct {
	DataDir     string
	AssetDir    string // geoip.dat / geosite.dat
	TunName     string
	BindIface   string // ПК: имя реального адаптера (исходящие соединения Xray мимо туннеля)
	Platform    string
	NoTUN       bool // стенд проверки: ядро без TUN, только выходы и проверки
	LogLevel    string
	ErrorLog    string // файл лога ошибок Xray (для диагностики паник в TUN)
	AccessLog   string // журнал соединений Xray (диагностика маршрутов; пусто — выключен)
	Bypass      Bypass
	NetKey      func() string                 // «отпечаток» текущей сети — для запоминания удачного обхода
	OnState     func(State)                   // вызывается при каждом изменении
	Logf        func(format string, a ...any) //
	Event       func(msg string)              // журнал проблем (Android: trouble.log): сводки проверок, смены состояния
	BeforeStart func() error                  // Android: передать Xray дескриптор TUN
	AfterStart  func() error                  // ПК: адрес/маршруты/DNS адаптера
}

type State struct {
	State    string            `json:"state"` // off | connecting | on
	Detail   string            `json:"detail"`
	TCP      string            `json:"tcp,omitempty"`
	UDP      string            `json:"udp,omitempty"`
	Services map[string]string `json:"services,omitempty"`
	Bypass   string            `json:"bypass,omitempty"`
	PingMs   int               `json:"ping,omitempty"`
}

type Engine struct {
	opt     Options
	profile *Profile

	mu       sync.Mutex
	inst     *xcore.Instance
	switches map[string]*switchHandler
	targets  map[string]string // выбор переключателей переживает перезапуск ядра
	ctx      context.Context
	cancel   context.CancelFunc
	kick     chan struct{} // «проверь сейчас» (смена сети) — протоколы
	kickSvc  chan struct{} // то же для сервисов (обход DPI)
	st       State
	running  bool

	cache cacheFile
}

type cacheFile struct {
	Bypass  map[string]string   `json:"bypass"`  // сеть -> вариант обхода
	Checked map[string]int64    `json:"checked"` // сеть -> когда подобран (unix), «none» перепроверяется
	LatMs   map[string]int64    `json:"lat_ms"`  // сеть -> время ответа выбранного обхода (нет — подобран по-старому, перепроверить)
	Pass    map[string][]string `json:"pass"`    // сеть -> сервисы, прошедшие с выбранным обходом (остальные — через VPN)
	Last    string              `json:"last"`
}

func NewEngine(opt Options) *Engine {
	if opt.LogLevel == "" {
		opt.LogLevel = "warning"
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	if opt.NetKey == nil {
		opt.NetKey = func() string { return "default" }
	}
	e := &Engine{opt: opt, targets: map[string]string{}, st: State{State: "off"}}
	if b, err := os.ReadFile(filepath.Join(opt.DataDir, "cache.json")); err == nil {
		_ = json.Unmarshal(b, &e.cache)
	}
	if e.cache.Bypass == nil {
		e.cache.Bypass = map[string]string{}
	}
	if e.cache.Checked == nil {
		e.cache.Checked = map[string]int64{}
	}
	if e.cache.LatMs == nil {
		e.cache.LatMs = map[string]int64{}
	}
	if e.cache.Pass == nil {
		e.cache.Pass = map[string][]string{}
	}
	return e
}

func (e *Engine) saveCache() {
	b, _ := json.MarshalIndent(e.cache, "", " ")
	_ = writeFileAtomic(filepath.Join(e.opt.DataDir, "cache.json"), b)
}

func (e *Engine) State() State { e.mu.Lock(); defer e.mu.Unlock(); return e.st }

func (e *Engine) setState(f func(*State)) {
	e.mu.Lock()
	old := e.st
	f(&e.st)
	s := e.st
	e.mu.Unlock()
	if e.opt.Event != nil && (old.State != s.State || old.TCP != s.TCP || old.UDP != s.UDP) {
		e.opt.Event(fmt.Sprintf("состояние %s -> %s (%s) tcp=%s udp=%s", old.State, s.State, s.Detail, s.TCP, s.UDP))
	}
	if e.opt.OnState != nil && fmt.Sprint(old) != fmt.Sprint(s) {
		e.opt.OnState(s)
	}
}

// Start запускает ядро и фоновые проверки. Возвращается сразу после запуска ядра (состояние — connecting).
func (e *Engine) Start(p *Profile) error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return nil
	}
	e.profile = p
	e.running = true
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.kick = make(chan struct{}, 1)
	e.kickSvc = make(chan struct{}, 1)
	e.mu.Unlock()

	e.setState(func(s *State) { *s = State{State: "connecting", Detail: "Подбираю лучший путь"} })
	// до первых проверок: основной протокол, сервисы — через VPN (Hysteria2 с BBR надёжен везде)
	for k, v := range map[string]string{swTCP: tagHy2, swUDP: tagHy2} {
		if e.targets[k] == "" {
			e.targets[k] = v
		}
	}
	for _, s := range p.Services {
		if e.targets[swService(s.ID)] == "" {
			e.targets[swService(s.ID)] = swTCP
		}
	}
	if err := e.startCore(); err != nil {
		e.Stop()
		return err
	}
	go e.watchLoop(e.ctx)
	go e.transportLoop(e.ctx)
	go e.serviceLoop(e.ctx)
	go e.pingLoop(e.ctx)
	return nil
}

func (e *Engine) startCore() error {
	if e.opt.AssetDir != "" {
		os.Setenv("XRAY_LOCATION_ASSET", e.opt.AssetDir)
		os.Setenv("xray.location.asset", e.opt.AssetDir)
	}
	cfgJSON, _ := json.Marshal(e.buildConfig())
	if e.opt.LogLevel == "debug" {
		_ = os.WriteFile(filepath.Join(e.opt.DataDir, "xray-config.json"), cfgJSON, 0o600)
	}
	jc, err := serial.DecodeJSONConfig(bytes.NewReader(cfgJSON))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	pb, err := jc.Build()
	if err != nil {
		return fmt.Errorf("config build: %w", err)
	}
	if e.opt.BeforeStart != nil {
		if err := e.opt.BeforeStart(); err != nil {
			return err
		}
	}
	inst, err := xcore.New(pb)
	if err != nil {
		return fmt.Errorf("core: %w", err)
	}
	om := inst.GetFeature(outbound.ManagerType()).(outbound.Manager)
	sw := map[string]*switchHandler{}
	e.mu.Lock()
	for tag, target := range e.targets {
		sw[tag] = newSwitch(tag, target, om)
	}
	e.mu.Unlock()
	for _, h := range sw {
		if err := om.AddHandler(context.Background(), h); err != nil {
			inst.Close()
			return err
		}
	}
	if err := inst.Start(); err != nil {
		inst.Close()
		return fmt.Errorf("core start: %w", err)
	}
	e.mu.Lock()
	e.inst, e.switches = inst, sw
	e.mu.Unlock()
	if e.opt.AfterStart != nil {
		if err := e.opt.AfterStart(); err != nil {
			e.mu.Lock()
			e.inst = nil
			e.mu.Unlock()
			inst.Close()
			return err
		}
	}
	return nil
}

// Restart перезапускает ядро (смена сети/адаптера) с сохранением всех выборов. Длится доли секунды.
func (e *Engine) Restart(bindIface string) error {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return nil
	}
	inst := e.inst
	e.inst = nil
	e.opt.BindIface = bindIface
	e.mu.Unlock()
	if inst != nil {
		inst.Close()
	}
	hysteria.ResetClients() // клиент Hysteria2 глобальный: иначе остался бы со старым адаптером
	err := e.startCore()
	e.Kick()
	return err
}

// NetworkChanged — сменилась сеть без пересоздания ядра (Android: Wi-Fi <-> LTE). Соединения Hysteria2
// привязаны к старой сети и висели бы до таймаута простоя — закрываем их сразу, приложения переподключатся.
func (e *Engine) NetworkChanged() {
	hysteria.ResetClients()
	e.Kick()
}

// Kick — проверить всё прямо сейчас (например, после смены сети).
func (e *Engine) Kick() {
	e.mu.Lock()
	ks := []chan struct{}{e.kick, e.kickSvc}
	e.mu.Unlock()
	for _, k := range ks {
		if k != nil {
			select {
			case k <- struct{}{}:
			default:
			}
		}
	}
}

func (e *Engine) Stop() {
	e.mu.Lock()
	if e.cancel != nil {
		e.cancel()
	}
	inst := e.inst
	e.inst, e.running = nil, false
	e.mu.Unlock()
	if inst != nil {
		inst.Close()
	}
	if e.opt.Bypass != nil {
		e.opt.Bypass.Stop()
	}
	e.setState(func(s *State) { *s = State{State: "off"} })
}

func (e *Engine) Running() bool { e.mu.Lock(); defer e.mu.Unlock(); return e.running }

func (e *Engine) SetBindIface(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opt.BindIface = name
}

func (e *Engine) setTarget(sw, target string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.targets[sw] == target {
		return
	}
	e.opt.Logf("%s -> %s", sw, target)
	e.targets[sw] = target
	if h := e.switches[sw]; h != nil {
		h.Set(target)
	}
}

func (e *Engine) target(sw string) string { e.mu.Lock(); defer e.mu.Unlock(); return e.targets[sw] }

// ---------------- проверки через конкретный выход Xray

func (e *Engine) instance() *xcore.Instance { e.mu.Lock(); defer e.mu.Unlock(); return e.inst }

func (e *Engine) dialVia(ctx context.Context, tag string, dest xnet.Destination) (xnet.Conn, error) {
	inst := e.instance()
	if inst == nil {
		return nil, errors.New("core stopped")
	}
	ctx = session.SetForcedOutboundTagToContext(ctx, tag)
	return xcore.Dial(ctx, inst, dest)
}

func (e *Engine) httpVia(tag string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{DisableKeepAlives: true, TLSHandshakeTimeout: timeout,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, addr string) (xnet.Conn, error) {
				dest, err := xnet.ParseDestination("tcp:" + addr)
				if err != nil {
					return nil, err
				}
				return e.dialVia(ctx, tag, dest)
			}}}
}

// probeHTTP: GET url через выход tag; minBytes>0 — нужно успеть получить столько данных
// (замедление провайдером видно именно так: соединение есть, а данные «ползут»).
func (e *Engine) probeHTTP(ctx context.Context, tag, url string, minBytes int, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")
	t0 := time.Now()
	resp, err := e.httpVia(tag, timeout).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if minBytes > 0 {
		n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, int64(minBytes)))
		if n < int64(minBytes) && err == nil && resp.ContentLength >= 0 && n >= resp.ContentLength {
			return time.Since(t0), nil // ответ целиком меньше порога — тоже годится
		}
		if n < int64(minBytes) {
			return 0, fmt.Errorf("got %d of %d bytes: %v", n, minBytes, err)
		}
	}
	return time.Since(t0), nil
}

// probeUDP: DNS-запрос к 8.8.8.8 или 77.88.8.8 через выход tag.
func (e *Engine) probeUDP(ctx context.Context, tag string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	targets := []string{"8.8.8.8", "77.88.8.8"}
	var lastErr error
	for _, ip := range targets {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c, err := e.dialVia(ctx, tag, xnet.UDPDestination(xnet.ParseAddress(ip), 53))
		if err != nil {
			lastErr = err
			continue
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-done:
			case <-ctx.Done():
				c.Close()
			case <-time.After(timeout / 2):
				c.Close()
			}
		}()

		q := []byte{0x12, 0x34, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}
		if _, err := c.Write(q); err == nil {
			buf := make([]byte, 1500)
			n, err := c.Read(buf)
			close(done)
			c.Close()
			if err == nil && n > 12 && buf[0] == 0x12 && buf[1] == 0x34 {
				return nil
			}
		} else {
			close(done)
			c.Close()
		}
		lastErr = errors.New("no udp reply")
	}
	return lastErr
}

// ---------------- выбор протокола

type streak struct {
	ok, fail int
	seen     bool   // хоть раз работал
	hist     []bool // последние проверки (до 10)
}

func (e *Engine) transportLoop(ctx context.Context) {
	tcpOrder := []string{tagHy2, tagVLESS, tagXHTTP}
	udpOrder := []string{tagHy2, tagVLESS}
	tcp := map[string]*streak{}
	udp := map[string]*streak{}
	for _, t := range tcpOrder {
		tcp[t], udp[t] = &streak{}, &streak{}
	}
	downRounds := 0
	first := true
	degraded := true // текущий выход не отвечал на прошлом круге: ответ приоритетного протокола переключает сразу
	// протокол, который стабильно не проходит (дома VLESS рвут 9 из 10), проверяем раз в 3 мин, а не каждые
	// 20 с: лишние оборванные соединения к серверу только привлекают внимание DPI. Смена сети — всё заново.
	next := map[string]time.Time{}
	round := 0
	for {
		var wg sync.WaitGroup
		var mu sync.Mutex
		tcpOK, udpOK := map[string]bool{}, map[string]bool{}
		tcpMs, udpMs := map[string]time.Duration{}, map[string]time.Duration{}
		probed := map[string]bool{}
		for _, t := range tcpOrder {
			if !first && time.Now().Before(next[t]) {
				continue
			}
			probed[t] = true
			wg.Add(2)
			go func(t string) {
				defer wg.Done()
				d, err := e.probeHTTP(ctx, t, "https://www.gstatic.com/generate_204", 0, 8*time.Second)
				mu.Lock()
				tcpOK[t], tcpMs[t] = err == nil, d
				mu.Unlock()
				if err != nil {
					e.opt.Logf("probe tcp %s: %v", t, err)
				}
				// как только любой протокол ответил на первом круге — подключаемся сразу, не дожидаясь остальных
				// (LTE после простоя: Hysteria2 отвечает со 2-го круга, а круг ждёт самый медленный из остальных ~25 с)
				if err == nil && (first || (degraded && t == tcpOrder[0])) {
					e.setTarget(swTCP, t)
					e.markOn()
				}
			}(t)
			go func(t string) {
				defer wg.Done()
				if indexOf(udpOrder, t) < 0 {
					return // UDP поверх XHTTP (TCP) для игр и голоса не годится — не проверяем и не выбираем
				}
				t0 := time.Now()
				err := e.probeUDP(ctx, t, 6*time.Second)
				mu.Lock()
				udpOK[t], udpMs[t] = err == nil, time.Since(t0)
				mu.Unlock()
				if err != nil {
					e.opt.Logf("probe udp %s: %v", t, err)
				}
				// первый круг: UDP сразу на Hysteria2, если он ответил (иначе «кто первый» — и UDP
				// застревал на запасном протоколе); остальное решит choose() после круга
				if err == nil && (first || degraded) && t == udpOrder[0] {
					e.setTarget(swUDP, t)
				}
			}(t)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		first = false
		round++
		e.roundEvent(round, tcpOrder, udpOrder, probed, tcpOK, udpOK, tcpMs, udpMs)
		for _, t := range tcpOrder {
			if !probed[t] {
				continue
			}
			upd(tcp[t], tcpOK[t])
			upd(udp[t], udpOK[t])
			if tcp[t].fail >= 4 && t != e.target(swTCP) {
				next[t] = time.Now().Add(3 * time.Minute)
			} else {
				delete(next, t)
			}
		}
		if best := choose(tcpOrder, tcp, e.target(swTCP)); best != "" {
			e.setTarget(swTCP, best)
		}
		if best := choose(udpOrder, udp, e.target(swUDP)); best != "" {
			e.setTarget(swUDP, best)
		}
		anyOK := false
		for _, v := range tcpOK {
			anyOK = anyOK || v
		}
		if anyOK {
			downRounds = 0
			e.markOn()
		} else {
			downRounds++
			if downRounds >= 2 {
				e.setState(func(s *State) {
					s.State, s.Detail = "connecting", "Нет связи с сервером, пробую снова"
				})
			}
		}
		degraded = !tcpOK[e.target(swTCP)] || !udpOK[e.target(swUDP)]
		wait := 20 * time.Second
		if !anyOK {
			wait = 3 * time.Second
		} else if !tcpOK[e.target(swTCP)] || !udpOK[e.target(swUDP)] {
			wait = 4 * time.Second // текущий протокол сбоит — проверяем часто: сбой после смены сети надо поймать быстро
		}
		select {
		case <-ctx.Done():
			return
		case <-e.kick:
			next = map[string]time.Time{}
			degraded = true
			// новая сеть — история старой не годится (иначе упавший при смене сети протокол держится «по заслугам»)
			for _, t := range tcpOrder {
				tcp[t], udp[t] = &streak{}, &streak{}
			}
			e.setState(func(s *State) {
				if s.State != "on" {
					s.Detail = "Подбираю лучший путь"
				}
			})
		case <-time.After(wait):
		}
	}
}

// watchLoop — сквозные проверки реальных сервисов через текущий выход VPN (Telegram, Google) раз в минуту.
// Пишется в журнал, если что-то не открылось или ответило медленнее 2 с, и «пульс» раз в 10 минут. Нужно, чтобы
// потом по журналу видеть, КОГДА и ЧТО именно «не грузило» (состояние туннеля само по себе этого не показывает).
func (e *Engine) watchLoop(ctx context.Context) {
	if e.opt.Event == nil {
		return
	}
	type target struct{ name, url string }
	list := []target{{"telegram", "https://web.telegram.org/"}, {"telegram-api", "https://api.telegram.org/"}, {"google", "https://www.google.com/generate_204"}}
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(60 * time.Second):
		}
		tag := e.target(swTCP)
		if tag == "" {
			continue
		}
		res := make([]string, len(list))
		bad := false
		var mu sync.Mutex
		var wg sync.WaitGroup
		for k, t := range list {
			wg.Add(1)
			go func(k int, t target) {
				defer wg.Done()
				d, err := e.probeHTTP(ctx, tag, t.url, 0, 10*time.Second)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil:
					res[k] = t.name + "=СБОЙ(" + shortErr(err) + ")"
					bad = true
				case d > 2*time.Second:
					res[k] = fmt.Sprintf("%s=%dмс(МЕДЛЕННО)", t.name, d.Milliseconds())
					bad = true
				default:
					res[k] = fmt.Sprintf("%s=%dмс", t.name, d.Milliseconds())
				}
			}(k, t)
		}
		wg.Wait()
		if bad || i%10 == 0 {
			e.opt.Event("сквозная проверка через " + tag + ": " + strings.Join(res, " "))
		}
	}
}

func shortErr(err error) string {
	m := err.Error()
	switch {
	case strings.Contains(m, "deadline exceeded"), strings.Contains(m, "timeout"):
		return "таймаут"
	case strings.Contains(m, "reset"):
		return "reset"
	case strings.Contains(m, "EOF"):
		return "EOF"
	case strings.Contains(m, "closed pipe"):
		return "закрыто"
	}
	if len(m) > 40 {
		m = m[len(m)-40:]
	}
	return m
}

// roundEvent — строка в журнал проблем: каждый круг, где что-то не прошло или было медленно (>1.5 с),
// и «пульс» раз в ~5 минут, когда всё хорошо. Нужен, чтобы потом по журналу увидеть деградацию.
func (e *Engine) roundEvent(round int, tcpOrder, udpOrder []string, probed, tcpOK, udpOK map[string]bool, tcpMs, udpMs map[string]time.Duration) {
	if e.opt.Event == nil {
		return
	}
	bad := false
	var b strings.Builder
	b.WriteString("круг " + fmt.Sprint(round) + " tcp[")
	for _, t := range tcpOrder {
		if !probed[t] {
			b.WriteString(t + "=пропущен ")
			continue
		}
		if tcpOK[t] {
			b.WriteString(fmt.Sprintf("%s=%dмс ", t, tcpMs[t].Milliseconds()))
			bad = bad || tcpMs[t] > 1500*time.Millisecond
		} else {
			b.WriteString(t + "=СБОЙ ")
			bad = bad || t == e.target(swTCP)
		}
	}
	b.WriteString("] udp[")
	for _, t := range udpOrder {
		if udpOK[t] {
			b.WriteString(fmt.Sprintf("%s=%dмс ", t, udpMs[t].Milliseconds()))
			bad = bad || udpMs[t] > 1500*time.Millisecond
		} else {
			b.WriteString(t + "=СБОЙ ")
			bad = bad || t == e.target(swUDP)
		}
	}
	b.WriteString("] выбрано tcp=" + e.target(swTCP) + " udp=" + e.target(swUDP))
	// сбой не текущего протокола — не деградация (VLESS на плохой сети сбоит всегда) — пишем только раз в 5 мин
	if bad || round%12 == 1 {
		e.opt.Event(b.String())
	}
}

func upd(s *streak, ok bool) {
	if ok {
		s.ok++
		s.fail = 0
		s.seen = true
	} else {
		s.fail++
		s.ok = 0
	}
	s.hist = append(s.hist, ok)
	if len(s.hist) > 10 {
		s.hist = s.hist[len(s.hist)-10:]
	}
}

// rate — доля удачных среди последних проверок и их число.
func (s *streak) rate() (float64, int) {
	n := len(s.hist)
	if n == 0 {
		return 0, 0
	}
	k := 0
	for _, v := range s.hist {
		if v {
			k++
		}
	}
	return float64(k) / float64(n), n
}

// choose выбирает выход по ДОЛЕ удачных проверок, а не по последней. В сетях, где провайдер рвёт
// часть соединений (СПб 2026-09: VLESS проходил 2-5 из 10), прежняя логика «две удачи подряд —
// возвращаемся на приоритетный» гоняла трафик по кругу vless→xhttp→hy2 и соединения висли.
//   - на более приоритетный переходим, только если он стабилен: ≥5 проверок и ≥95% удачных;
//   - текущий держим, пока у него ≥80% удачных и нет двух сбоев подряд;
//   - иначе — первый по приоритету с ≥80%, а если таких нет — с лучшей долей.
func choose(order []string, st map[string]*streak, cur string) string {
	good := func(s *streak) bool { r, n := s.rate(); return n > 0 && r >= 0.8 && s.fail < 2 }
	// текущий держим, пока он не заметно хуже другого: на плохой сети (LTE, 10% потерь) проходят все
	// протоколы через раз, и прежнее «две неудачи подряд — меняем» гоняло трафик туда-сюда
	rateOf := func(s *streak) float64 { r, _ := s.rate(); return r }
	strong := func(s *streak) bool { r, n := s.rate(); return n >= 5 && r >= 0.95 && s.fail == 0 }
	if c := st[cur]; c != nil && indexOf(order, cur) >= 0 {
		// текущий молчит две проверки подряд — это не «потери на плохой сети», а обрыв (смена сети, перезапуск
		// клиента): сразу уходим на тот, что отвечает прямо сейчас (приоритет, затем доля удачных)
		// (на плохой сети, где все проходят через раз, это не срабатывает: там доли удач близки)
		if c.fail >= 2 {
			best, bestR := "", -1.0
			for _, t := range order {
				if t == cur || st[t].fail != 0 || !st[t].seen {
					continue
				}
				if r := rateOf(st[t]); r > bestR {
					best, bestR = t, r
				}
			}
			if best != "" && (c.fail >= 4 || bestR >= rateOf(c)+0.3) {
				return best
			}
		}
		_, curN := c.rate()
		for _, t := range order {
			if t == cur {
				break
			}
			// более приоритетный: стабилен давно — или проверок пока мало у обоих (первые круги после
			// старта/смены сети: текущий выбран «кто первый ответил», а не по приоритету)
			if strong(st[t]) || (curN < 5 && good(st[t])) {
				return t
			}
		}
		if good(c) {
			return cur
		}
		if _, n := c.rate(); n >= 3 {
			cr := rateOf(c)
			stay := true
			for _, t := range order {
				if t != cur && rateOf(st[t]) >= cr+0.3 {
					stay = false
				}
			}
			if stay && cr >= 0.3 {
				return cur
			}
		}
	}
	for _, t := range order {
		if good(st[t]) {
			return t
		}
	}
	best, bestR := "", 0.0
	for _, t := range order {
		if r, _ := st[t].rate(); r > bestR+0.05 {
			best, bestR = t, r
		}
	}
	return best
}

func (e *Engine) markOn() {
	e.setState(func(s *State) {
		s.State = "on"
		s.TCP, s.UDP = e.targetUnlocked(swTCP), e.targetUnlocked(swUDP)
		s.Detail = e.detailUnlocked(s.PingMs)
	})
}

func (e *Engine) targetUnlocked(sw string) string { return e.targets[sw] } // вызывается под e.mu (из setState)

func (e *Engine) detailUnlocked(ping int) string {
	c := "VPN"
	if e.profile != nil && e.profile.Country != "" {
		c = e.profile.Country
	}
	if ping > 0 {
		return fmt.Sprintf("%s · %d мс", c, ping)
	}
	return c
}

// ---------------- пинг до сервера (напрямую, как видит сеть)

func (e *Engine) pingLoop(ctx context.Context) {
	addr := fmt.Sprintf("%s:%d", e.profile.Server, e.profile.Port)
	for {
		var samples []int
		for i := 0; i < 3; i++ {
			t0 := time.Now()
			c, err := DirectDialer.DialContext(ctx, "tcp", addr)
			if err == nil {
				samples = append(samples, int(time.Since(t0).Milliseconds()))
				c.Close()
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(samples) > 0 {
			sort.Ints(samples)
			ms := samples[0]
			if ms < 1 {
				ms = 1
			}
			e.setState(func(s *State) {
				s.PingMs = ms
				if s.State == "on" {
					s.Detail = e.detailUnlocked(ms)
				}
			})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

// ---------------- сервисы напрямую / через обход DPI

// probeService проверяет сервис через выход tag; возвращает время самой медленной из его проб.
func (e *Engine) probeService(ctx context.Context, s Service, tag string) (time.Duration, error) {
	if len(s.Probes) == 0 {
		return 0, nil
	}
	var wg sync.WaitGroup
	errs := make([]error, len(s.Probes))
	durs := make([]time.Duration, len(s.Probes))
	for i, p := range s.Probes {
		wg.Add(1)
		go func(i int, p Probe) {
			defer wg.Done()
			d, err := e.probeHTTP(ctx, tag, p.URL, p.MinBytes, 9*time.Second)
			// медленный ответ — тоже провал: так выглядит обход, срабатывающий лишь после 5-секундного
			// таймаута (ByeDPI --auto=torst), — видео и страницы с ним «думают» по 5 с
			if err == nil && d > 4500*time.Millisecond {
				err = fmt.Errorf("медленно: %v", d.Round(100*time.Millisecond))
			}
			errs[i], durs[i] = err, d
		}(i, p)
	}
	wg.Wait()
	var slowest time.Duration
	for i, err := range errs {
		if err != nil {
			return 0, fmt.Errorf("%s: %w", s.Probes[i].URL, err)
		}
		slowest = max(slowest, durs[i])
	}
	return slowest, nil
}

// Контрольные сайты для прямого пути: если не открываются и они, дело не в DPI, а в самом прямом пути
// (нет интернета, брандмауэр, «белые списки» мобильного оператора) — такой результат не запоминаем.
var directControl = []string{"https://ya.ru/", "https://vk.com/"}

const (
	svcInterval = 10 * time.Minute       // обычная перепроверка обхода (пробы — ~0.5 МБ, на LTE не тратим зря)
	fastBypass  = 800 * time.Millisecond // обход «быстрый»: самый медленный сервис ответил за это время
	svcRetry    = 20 * time.Second       // после сбоя — быстро, чтобы видео не висело минутами
	noneRetry   = 6 * time.Hour          // «обход в этой сети не работает» — перепроверить через столько
	directRetry = 2 * time.Minute        // прямой путь не работал вовсе — подбор повторить через столько
)

func (e *Engine) directOK(ctx context.Context) bool {
	for _, u := range directControl {
		if _, err := e.probeHTTP(ctx, tagDirect, u, 0, 8*time.Second); err == nil {
			return true
		} else {
			e.opt.Logf("direct control %s: %v", u, err)
		}
	}
	return false
}

func (e *Engine) serviceLoop(ctx context.Context) {
	p := e.profile
	var direct, bypass []Service
	for _, s := range p.Services {
		if s.Mode == "bypass" && e.opt.Bypass != nil {
			bypass = append(bypass, s)
		} else {
			direct = append(direct, s)
		}
	}
	fails := map[string]int{}
	oks := map[string]int{}       // удачные проверки подряд (возврат из VPN в обход — после 3)
	selected := map[string]bool{} // сервисы, прошедшие при подборе: только их поломка — повод подбирать заново
	strategy := -1                // текущий вариант обхода
	bypassTag := ""
	netKey := ""
	lastSearch := time.Time{}
	directBroken := false // последний подбор не состоялся: прямой путь не работал вовсе

	toVPN := func() {
		for _, s := range bypass {
			e.setTarget(swService(s.ID), swTCP)
		}
	}
	publish := func() {
		e.setState(func(st *State) {
			st.Services = map[string]string{}
			for _, s := range p.Services {
				st.Services[s.ID] = e.targets[swService(s.ID)]
			}
			if strategy > 0 {
				st.Bypass = e.opt.Bypass.Strategies()[strategy]
			} else {
				st.Bypass = ""
			}
		})
	}
	// applyCached — как было в этой сети в прошлый раз (сервисы сразу идут напрямую, проверка — следом)
	applyCached := func() bool {
		name, ok := e.cache.Bypass[netKey]
		if !ok {
			return false
		}
		if name == "none" {
			if time.Since(time.Unix(e.cache.Checked[netKey], 0)) > noneRetry {
				return false // давно — подобрать заново
			}
			strategy, bypassTag = -1, ""
			_, _ = e.opt.Bypass.Apply(ctx, -1)
			toVPN()
			return true
		}
		i := indexOf(e.opt.Bypass.Strategies(), name)
		if _, ok := e.cache.LatMs[netKey]; i < 0 || !ok {
			return false // подобран без учёта скорости — подобрать заново
		}
		tag, err := e.opt.Bypass.Apply(ctx, i)
		if err != nil {
			return false
		}
		strategy, bypassTag = i, tag
		pass, known := e.cache.Pass[netKey]
		selected = map[string]bool{}
		for _, s := range bypass {
			selected[s.ID] = !known || indexOf(pass, s.ID) >= 0
			if selected[s.ID] {
				e.setTarget(swService(s.ID), tag)
			} else {
				e.setTarget(swService(s.ID), swTCP)
			}
		}
		return true
	}

	search := func() {
		lastSearch = time.Now()
		if !e.directOK(ctx) {
			// прямой путь не работает совсем — обход тут ни при чём; сервисы через VPN, подбор повторим позже
			e.opt.Logf("bypass: прямой путь недоступен, сервисы через VPN, повтор через %v", directRetry)
			directBroken = true
			if strategy < 0 {
				toVPN()
			}
			return
		}
		directBroken = false
		// пока перебираем варианты, живой трафик сервисов — через VPN (иначе видео рвётся на неудачных вариантах)
		toVPN()
		names := e.opt.Bypass.Strategies()
		order := make([]int, 0, len(names))
		if strategy >= 0 {
			order = append(order, strategy)
		}
		if i := indexOf(names, e.cache.Last); i >= 0 && i != strategy {
			order = append(order, i)
		}
		for i := range names {
			if i != strategy && names[i] != e.cache.Last {
				order = append(order, i)
			}
		}
		// лучший — больше сервисов, при равенстве — быстрее (стратегии с перестановкой пакетов работают, но
		// каждое новое соединение ждёт повтора TCP ~0.2-0.5 с — Shorts и превью «думают»). Нашли рабочую и
		// быструю — дальше не ищем; рабочую, но медленную — проверяем остальные и берём самую быструю.
		best, bestScore, bestTag := -1, 0, ""
		var bestLat time.Duration
		var bestPass map[string]bool
		for _, i := range order {
			if ctx.Err() != nil {
				return
			}
			tag, err := e.opt.Bypass.Apply(ctx, i)
			if err != nil {
				e.opt.Logf("bypass %s: %v", names[i], err)
				continue
			}
			// очки с приоритетом по порядку сервисов в подписке (YouTube > Twitch > Discord): вариант, где
			// проходит YouTube, лучше варианта без YouTube, сколько бы остальных он ни пропускал
			pass := map[string]bool{}
			score, passed := 0, 0
			var lat time.Duration // самый медленный из прошедших сервисов
			var wg sync.WaitGroup
			var mu sync.Mutex
			for si, s := range bypass {
				wg.Add(1)
				go func(si int, s Service) {
					defer wg.Done()
					d, err := e.probeService(ctx, s, tag)
					mu.Lock()
					defer mu.Unlock()
					if err == nil {
						pass[s.ID] = true
						score += 1 << (len(bypass) - 1 - si)
						passed++
						lat = max(lat, d)
					} else {
						e.opt.Logf("bypass %s / %s: %v", names[i], s.ID, err)
					}
				}(si, s)
			}
			wg.Wait()
			e.opt.Logf("bypass %s: %d/%d за %v", names[i], passed, len(bypass), lat.Round(10*time.Millisecond))
			if score > bestScore || (score == bestScore && score > 0 && lat < bestLat) {
				best, bestScore, bestTag, bestPass, bestLat = i, score, tag, pass, lat
			}
			if passed == len(bypass) && lat <= fastBypass {
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
		e.cache.Checked[netKey] = time.Now().Unix()
		if best < 0 {
			e.opt.Bypass.Apply(ctx, -1)
			strategy, bypassTag = -1, ""
			e.cache.Bypass[netKey] = "none"
			e.saveCache()
			toVPN()
			return
		}
		if _, err := e.opt.Bypass.Apply(ctx, best); err != nil {
			return
		}
		strategy, bypassTag = best, bestTag
		e.cache.Bypass[netKey] = names[best]
		e.cache.LatMs[netKey] = bestLat.Milliseconds()
		e.cache.Pass[netKey] = nil
		selected = map[string]bool{}
		for _, s := range bypass {
			if bestPass[s.ID] {
				e.cache.Pass[netKey] = append(e.cache.Pass[netKey], s.ID)
				selected[s.ID] = true
			}
		}
		e.cache.Last = names[best]
		e.saveCache()
		for _, s := range bypass {
			fails[s.ID], oks[s.ID] = 0, 0
			if bestPass[s.ID] {
				e.setTarget(swService(s.ID), bypassTag)
			} else {
				e.setTarget(swService(s.ID), swTCP)
			}
		}
	}

	// check возвращает true, если что-то не прошло (тогда следующая проверка — скоро)
	check := func() (trouble bool) {
		defer publish()
		// прямые (загрузки Steam и т.п.): напрямую, пока работает
		for _, s := range direct {
			_, err := e.probeService(ctx, s, tagDirect)
			if err == nil {
				fails[s.ID] = 0
				e.setTarget(swService(s.ID), tagDirect)
			} else if fails[s.ID]++; fails[s.ID] >= 2 || e.target(swService(s.ID)) == swTCP {
				e.setTarget(swService(s.ID), swTCP)
				trouble = true
			}
		}
		if len(bypass) == 0 {
			return
		}
		if k := e.opt.NetKey(); k != netKey {
			e.opt.Logf("bypass: сеть %q", k)
			netKey = k
			if !applyCached() {
				search()
				return directBroken
			}
		}
		if strategy < 0 {
			name, cached := e.cache.Bypass[netKey]
			switch {
			case directBroken && time.Since(lastSearch) >= directRetry:
				search()
			case !cached && !directBroken:
				search()
			case cached && name == "none" && time.Since(time.Unix(e.cache.Checked[netKey], 0)) > noneRetry:
				search()
			}
			return directBroken
		}
		broken := 0
		for _, s := range bypass {
			sw := swService(s.ID)
			if _, err := e.probeService(ctx, s, bypassTag); err == nil {
				fails[s.ID] = 0
				oks[s.ID]++
				// из VPN обратно в обход — только после 3 удачных подряд (иначе сервис «прыгает» туда-обратно)
				if e.target(sw) == bypassTag || oks[s.ID] >= 3 {
					e.setTarget(sw, bypassTag)
				}
			} else {
				if selected[s.ID] {
					e.opt.Logf("check %s: %v", s.ID, err)
				}
				fails[s.ID]++
				oks[s.ID] = 0
				if fails[s.ID] >= 2 {
					e.setTarget(sw, swTCP)
				}
				// частые проверки и новый подбор — только если сломалось то, что при подборе работало
				// (сервис, не прошедший подбор, и так в VPN — его неудачи ничего не меняют)
				if selected[s.ID] {
					trouble = true
					if fails[s.ID] >= 2 {
						broken++
					}
				}
			}
		}
		if broken > 0 && time.Since(lastSearch) > 30*time.Minute {
			search()
		}
		return
	}

	// первая проверка — почти сразу; поиск обхода, если в этой сети ещё не искали
	select {
	case <-ctx.Done():
		return
	case <-time.After(1500 * time.Millisecond):
	}
	if len(bypass) > 0 {
		netKey = e.opt.NetKey()
		if !applyCached() {
			search()
		}
	}
	trouble := check()
	for {
		wait := svcInterval
		if trouble {
			wait = svcRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-e.kickSvc:
			// смена сети: дать сети подняться и перепроверить (новая сеть — свой вариант обхода)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		case <-time.After(wait):
		}
		trouble = check()
	}
}

func indexOf(list []string, s string) int {
	if s == "" {
		return -1
	}
	for i, v := range list {
		if strings.EqualFold(v, s) {
			return i
		}
	}
	return -1
}
