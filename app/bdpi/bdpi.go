// Package bdpi — обход DPI без root (Android): ByeDPI как локальный SOCKS + фрагментация TLS средствами Xray.
// Движок перебирает варианты, проверяя сервисы (YouTube, Discord), и запоминает удачный для каждой сети.
package bdpi

import (
	"context"
	"fmt"
	"net"

	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type obj = map[string]any

type strategy struct {
	name string
	args []string // аргументы ByeDPI; nil — вариант без ByeDPI
	tag  string   // выход Xray
}

// Порядок: сначала проверенные в реальной сети (СПб, 2026-10). Важно: YouTube/Chrome (Cronet) шлют
// БОЛЬШОЙ ClientHello (постквантовый X25519MLKEM768, ~1.8 КБ, 2 TCP-сегмента) — стратегии, рассчитанные
// на маленький ClientHello (-s1 -q1), его не пробивают (TLS рвётся через 5 с). Пробивают разбиения
// относительно SNI во втором сегменте: -q1+s -s3+s/-s5+s + drop-sack (-Y). Замер: видео googlevideo
// Go-клиентом с MLKEM ~45 Мбит/с. Пробы движка — на Go (тоже MLKEM), т.е. проверяют ровно этот случай.
var strategies = []strategy{
	{"none", nil, "direct"},
	{"bd-q3-sack", []string{"-s1", "-q1+s", "-s3+s", "-Y"}, "bdpi"},
	{"bd-q5-sack", []string{"-s1", "-q1+s", "-s5+s", "-Y"}, "bdpi"},
	{"bd-q35-sack", []string{"-s1", "-q1+s", "-s3+s", "-s5+s", "-Y"}, "bdpi"},
	{"bd-sq-sack", []string{"-s1", "-q1", "-Y"}, "bdpi"},
	{"bd-auto-sack", []string{"-s1", "-q1", "-Y", "-Ar", "-s5", "-o1+s", "-At", "-f-1", "-r1+s", "-As", "-s1", "-o1+s", "-s-1", "-An"}, "bdpi"},
	{"bd-disorder", []string{"--disorder", "1", "--auto=torst", "--tlsrec", "1+s"}, "bdpi"},
	{"bd-split-rec", []string{"--split", "1+s", "--disorder", "3+s", "--tlsrec", "3+s"}, "bdpi"},
	{"bd-fake", []string{"--disorder", "1", "--fake", "-1", "--ttl", "5", "--fake-tls-mod", "rand", "--tlsrec", "1+s"}, "bdpi"},
	{"xr-frag", nil, "frag1"},
	{"xr-frag-small", nil, "frag2"},
}

type B struct {
	Bin  string // путь к исполняемому ByeDPI (libbyedpi.so в nativeLibraryDir)
	Port int
	Logf func(string, ...any)

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{} // закрывается, когда процесс ByeDPI завершился
	cur  int
}

func New(bin string, port int, logf func(string, ...any)) *B {
	return &B{Bin: bin, Port: port, Logf: logf, cur: -1}
}

func (b *B) Outbounds() []obj {
	so := func() obj { return obj{} } // без tcpFastOpen — см. core/xconf.go
	return []obj{
		{"tag": "bdpi", "protocol": "socks", "targetStrategy": "UseIPv4",
			"settings":       obj{"servers": []obj{{"address": "127.0.0.1", "port": b.Port}}},
			"streamSettings": obj{"sockopt": so()}},
		{"tag": "frag1", "protocol": "freedom", "settings": obj{"domainStrategy": "UseIPv4",
			"fragment": obj{"packets": "tlshello", "length": "100-200", "interval": "10-20"}},
			"streamSettings": obj{"sockopt": so()}},
		{"tag": "frag2", "protocol": "freedom", "settings": obj{"domainStrategy": "UseIPv4",
			"fragment": obj{"packets": "tlshello", "length": "1-3", "interval": "1-2"}},
			"streamSettings": obj{"sockopt": so()}},
	}
}

func (b *B) Strategies() []string {
	n := make([]string, len(strategies))
	for i, s := range strategies {
		n[i] = s.name
	}
	return n
}

func (b *B) Apply(ctx context.Context, i int) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if i < 0 || i >= len(strategies) {
		b.stopLocked()
		b.cur = -1
		return "", nil
	}
	s := strategies[i]
	if b.cur == i && (s.args == nil || b.cmd != nil) {
		return s.tag, nil
	}
	b.stopLocked()
	b.cur = i
	if s.args == nil {
		return s.tag, nil
	}
	args := append([]string{"-i", "127.0.0.1", "-p", strconv.Itoa(b.Port), "-U", "-c", "1024", "-T", "5"}, s.args...)
	cmd := exec.Command(b.Bin, args...)
	cmd.SysProcAttr = sysProcAttr()
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		b.cur = -1
		return "", fmt.Errorf("byedpi: %w", err)
	}
	b.cmd = cmd
	done := make(chan struct{})
	b.done = done
	go func(c *exec.Cmd) { _ = c.Wait(); close(done) }(cmd)
	// ждём, пока ИМЕННО ЭТОТ ByeDPI начнёт слушать порт (если он сразу вышел — ошибка)
	for t := 0; t < 40; t++ {
		select {
		case <-done:
			b.cmd, b.done, b.cur = nil, nil, -1
			return "", fmt.Errorf("byedpi завершился при запуске")
		default:
		}
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(b.Port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return s.tag, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	b.stopLocked()
	b.cur = -1
	return "", fmt.Errorf("byedpi не запустился")
}

// stopLocked останавливает ByeDPI и ЖДЁТ, пока порт освободится. Раньше старый процесс добивался
// через секунду в фоне: новый не мог занять порт, проверка «порт слушает» видела ещё живой старый —
// и после его смерти все стратегии при подборе падали («closed pipe»).
func (b *B) stopLocked() {
	if b.cmd != nil && b.cmd.Process != nil {
		p, done := b.cmd.Process, b.done
		_ = p.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(700 * time.Millisecond):
			_ = p.Kill()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
		}
	}
	b.cmd, b.done = nil, nil
	// порт свободен? (на всякий случай — до 1 с)
	for t := 0; t < 20; t++ {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(b.Port), 50*time.Millisecond)
		if err != nil {
			return
		}
		c.Close()
		time.Sleep(50 * time.Millisecond)
	}
}

func (b *B) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
	b.cur = -1
}


