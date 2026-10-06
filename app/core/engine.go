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

	xcore "github.com/xtls/xray-core/core"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/features/outbound"
	"github.com/xtls/xray-core/infra/conf/serial"
	_ "github.com/xtls/xray-core/main/distro/all"
)

// Bypass — встроенный обход DPI для сервисов mode=bypass (ПК: zapret/winws, Android: ByeDPI).
type Bypass interface {
	Outbounds() []obj                             // дополнительные выходы Xray (например, SOCKS до ByeDPI)
	Strategies() []string                         // варианты обхода по порядку; 0 — «без обхода»
	Apply(ctx context.Context, i int) (string, error) // включить вариант i, вернуть тег выхода; i<0 — выключить
	Stop()
}

type Options struct {
	DataDir   string
	AssetDir  string // geoip.dat / geosite.dat
	TunName   string
	BindIface string // ПК: имя реального адаптера (исходящие соединения Xray мимо туннеля)
	Platform  string
	LogLevel  string
	ErrorLog  string // файл лога ошибок Xray (для диагностики паник в TUN)
	AccessLog string // журнал соединений Xray (диагностика маршрутов; пусто — выключен)
	Bypass    Bypass
	NetKey    func() string                 // «отпечаток» текущей сети — для запоминания удачного обхода
	OnState   func(State)                   // вызывается при каждом изменении
	Logf      func(format string, a ...any) //
	BeforeStart func() error                // Android: передать Xray дескриптор TUN
	AfterStart  func() error                // ПК: адрес/маршруты/DNS адаптера
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
	kick     chan struct{} // «проверь сейчас» (смена сети)
	st       State
	running  bool

	cache cacheFile
}

type cacheFile struct {
	Bypass map[string]string `json:"bypass"` // сеть -> вариант обхода
	Last   string            `json:"last"`
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
	err := e.startCore()
	e.Kick()
	return err
}

// Kick — проверить всё прямо сейчас (например, после смены сети).
func (e *Engine) Kick() {
	e.mu.Lock()
	k := e.kick
	e.mu.Unlock()
	if k != nil {
		select {
		case k <- struct{}{}:
		default:
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
	for {
		var wg sync.WaitGroup
		var mu sync.Mutex
		tcpOK, udpOK := map[string]bool{}, map[string]bool{}
		for _, t := range tcpOrder {
			wg.Add(2)
			go func(t string) {
				defer wg.Done()
				_, err := e.probeHTTP(ctx, t, "https://www.gstatic.com/generate_204", 0, 5*time.Second)
				mu.Lock()
				tcpOK[t] = err == nil
				mu.Unlock()
				if err != nil {
					e.opt.Logf("probe tcp %s: %v", t, err)
				}
				// как только любой протокол ответил на первом круге — подключаемся сразу, не дожидаясь остальных
				if err == nil && first {
					e.setTarget(swTCP, t)
					e.markOn()
				}
			}(t)
			go func(t string) {
				defer wg.Done()
				err := e.probeUDP(ctx, t, 4*time.Second)
				mu.Lock()
				udpOK[t] = err == nil
				mu.Unlock()
				if err != nil {
					e.opt.Logf("probe udp %s: %v", t, err)
				}
				if err == nil && first {
					e.setTarget(swUDP, t)
				}
			}(t)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return
		}
		first = false
		for _, t := range tcpOrder {
			upd(tcp[t], tcpOK[t])
			upd(udp[t], udpOK[t])
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
		wait := 20 * time.Second
		if !anyOK {
			wait = 3 * time.Second
		} else if !tcpOK[e.target(swTCP)] || !udpOK[e.target(swUDP)] {
			wait = 15 * time.Second // текущий протокол сбоит — интервал 15 с без спама сети
		}
		select {
		case <-ctx.Done():
			return
		case <-e.kick:
			e.setState(func(s *State) {
				if s.State != "on" {
					s.Detail = "Подбираю лучший путь"
				}
			})
		case <-time.After(wait):
		}
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
	strong := func(s *streak) bool { r, n := s.rate(); return n >= 5 && r >= 0.95 && s.fail == 0 }
	if c := st[cur]; c != nil {
		for _, t := range order {
			if t == cur {
				break
			}
			if strong(st[t]) {
				return t
			}
		}
		if good(c) {
			return cur
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

func (e *Engine) probeService(ctx context.Context, s Service, tag string) error {
	if len(s.Probes) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	errs := make([]error, len(s.Probes))
	for i, p := range s.Probes {
		wg.Add(1)
		go func(i int, p Probe) {
			defer wg.Done()
			_, errs[i] = e.probeHTTP(ctx, tag, p.URL, p.MinBytes, 9*time.Second)
		}(i, p)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			return fmt.Errorf("%s: %w", s.Probes[i].URL, err)
		}
	}
	return nil
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
	strategy := -1 // текущий вариант обхода
	bypassTag := ""
	netKey := ""
	lastSearch := time.Time{}

	// сначала — как было в этой сети в прошлый раз (сервисы сразу идут напрямую, проверка — следом)
	if len(bypass) > 0 {
		netKey = e.opt.NetKey()
		if name, ok := e.cache.Bypass[netKey]; ok {
			if i := indexOf(e.opt.Bypass.Strategies(), name); i >= 0 {
				if tag, err := e.opt.Bypass.Apply(ctx, i); err == nil {
					strategy, bypassTag = i, tag
					for _, s := range bypass {
						e.setTarget(swService(s.ID), tag)
					}
				}
			}
		}
	}

	search := func() {
		lastSearch = time.Now()
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
		best, bestScore, bestTag := -1, 0, ""
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
			pass := map[string]bool{}
			score := 0
			var wg sync.WaitGroup
			var mu sync.Mutex
			for _, s := range bypass {
				wg.Add(1)
				go func(s Service) {
					defer wg.Done()
					err := e.probeService(ctx, s, tag)
					mu.Lock()
					defer mu.Unlock()
					if err == nil {
						pass[s.ID] = true
						score++
					} else {
						e.opt.Logf("bypass %s / %s: %v", names[i], s.ID, err)
					}
				}(s)
			}
			wg.Wait()
			e.opt.Logf("bypass %s: %d/%d", names[i], score, len(bypass))
			if score > bestScore {
				best, bestScore, bestTag, bestPass = i, score, tag, pass
			}
			if score == len(bypass) {
				break
			}
		}
		if best < 0 {
			e.opt.Bypass.Apply(ctx, -1)
			strategy, bypassTag = -1, ""
			for _, s := range bypass {
				e.setTarget(swService(s.ID), swTCP)
			}
			return
		}
		if _, err := e.opt.Bypass.Apply(ctx, best); err != nil {
			return
		}
		strategy, bypassTag = best, bestTag
		e.cache.Bypass[netKey] = names[best]
		e.cache.Last = names[best]
		e.saveCache()
		for _, s := range bypass {
			if bestPass[s.ID] {
				e.setTarget(swService(s.ID), bypassTag)
				fails[s.ID] = 0
			} else {
				e.setTarget(swService(s.ID), swTCP)
			}
		}
		e.setState(func(st *State) { st.Bypass = names[best] })
	}

	check := func() {
		// прямые (twitch, steam): напрямую, пока работает
		for _, s := range direct {
			err := e.probeService(ctx, s, tagDirect)
			if err == nil {
				fails[s.ID] = 0
				e.setTarget(swService(s.ID), tagDirect)
			} else if fails[s.ID]++; fails[s.ID] >= 2 || e.target(swService(s.ID)) == swTCP {
				e.setTarget(swService(s.ID), swTCP)
			}
		}
		if len(bypass) == 0 {
			return
		}
		if k := e.opt.NetKey(); k != netKey {
			netKey = k
			if name, ok := e.cache.Bypass[k]; ok {
				if i := indexOf(e.opt.Bypass.Strategies(), name); i >= 0 && i != strategy {
					if tag, err := e.opt.Bypass.Apply(ctx, i); err == nil {
						strategy, bypassTag = i, tag
					}
				}
			} else {
				lastSearch = time.Time{}
			}
		}
		if strategy < 0 {
			if time.Since(lastSearch) > 10*time.Minute {
				search()
			}
			return
		}
		broken, onVPN := 0, 0
		for _, s := range bypass {
			if err := e.probeService(ctx, s, bypassTag); err == nil {
				fails[s.ID] = 0
				e.setTarget(swService(s.ID), bypassTag)
			} else {
				e.opt.Logf("check %s: %v", s.ID, err)
				fails[s.ID]++
				if fails[s.ID] >= 2 {
					e.setTarget(swService(s.ID), swTCP)
				}
			}
			if fails[s.ID] >= 2 {
				broken++
			}
			if e.target(swService(s.ID)) == swTCP {
				onVPN++
			}
		}
		if broken > 0 && time.Since(lastSearch) > 30*time.Minute && strategy >= 0 {
			search()
		}
		e.setState(func(st *State) {
			st.Services = map[string]string{}
			for _, s := range p.Services {
				st.Services[s.ID] = e.targets[swService(s.ID)]
			}
		})
	}

	// первая проверка — сразу; поиск обхода, если в этой сети ещё не искали
	time.Sleep(1500 * time.Millisecond)
	if len(bypass) > 0 && strategy < 0 {
		search()
	}
	check()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Minute):
		}
		check()
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
