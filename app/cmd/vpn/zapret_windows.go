//go:build windows

package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"vpnapp/core"
)

// zapret — встроенный обход DPI на ПК (Flowseal zapret-discord-youtube: winws + WinDivert).
// Работает только на реальном адаптере (--wf-iface) и не трогает трафик к серверу VPN (--ipset-exclude-ip),
// иначе подмешивает пакеты в туннель. Стратегии — батники general*.bat из свежего релиза (сервер
// скачивает его сам): приложение перебирает их, проверяя YouTube и Discord, и запоминает удачную для сети.
type zapret struct {
	root    string // папка распакованного релиза
	server  string
	ifIndex uint32
	logf    func(string, ...any)

	strategies []zStrategy
	mu         sync.Mutex
	cmd        *exec.Cmd
	job        windows.Handle
	cur        int
}

type zStrategy struct {
	name string
	args []string
}

// prepareZapret скачивает (если нужно) и распаковывает релиз, разбирает стратегии.
func prepareZapret(ctx context.Context, dataDir string, ref core.FileRef, server string, logf func(string, ...any)) (*zapret, error) {
	zipPath := filepath.Join(dataDir, "zapret-win.zip")
	if ref.URL != "" {
		if _, err := core.EnsureFile(ctx, ref, zipPath); err != nil {
			logf("zapret download: %v", err)
		}
	}
	if _, err := os.Stat(zipPath); err != nil {
		return nil, fmt.Errorf("zapret ещё не скачан")
	}
	b, _ := os.ReadFile(zipPath)
	ver := core.SHA(b)[:12]
	root := filepath.Join(dataDir, "zapret", ver)
	if _, err := os.Stat(filepath.Join(root, "bin", "winws.exe")); err != nil {
		tmp := root + ".tmp"
		os.RemoveAll(tmp)
		if err := unzipStrip(zipPath, tmp); err != nil {
			return nil, err
		}
		os.RemoveAll(root)
		if err := os.Rename(tmp, root); err != nil {
			return nil, err
		}
		// старые версии — удалить (кроме той, что может работать прямо сейчас)
		if old, _ := filepath.Glob(filepath.Join(dataDir, "zapret", "*")); len(old) > 0 {
			for _, o := range old {
				if o != root {
					os.RemoveAll(o)
				}
			}
		}
	}
	z := &zapret{root: root, server: server, logf: logf, cur: -1}
	lists := filepath.Join(root, "lists")
	for name, content := range map[string]string{
		"ipset-exclude-user.txt": "203.0.113.113/32\r\n" + server + "/32\r\n",
		// Twitch (страница, видео, чат) — напрямую через обход, без рекламы VPN-региона
		"list-general-user.txt": "twitch.tv\r\nttvnw.net\r\njtvnw.net\r\ntwitchcdn.net\r\ntwitchsvc.net\r\n" +
			"ext-twitch.tv\r\nlive-video.net\r\n",
		"list-exclude-user.txt":  "domain.example.abc\r\n",
	} {
		_ = os.WriteFile(filepath.Join(lists, name), []byte(content), 0o644)
	}
	if err := z.parse(); err != nil {
		return nil, err
	}
	return z, nil
}

func unzipStrip(zipPath, dst string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		name := f.Name
		if i := strings.Index(name, "/"); i >= 0 { // первая папка архива (zapret-discord-youtube-x.y.z/)
			name = name[i+1:]
		}
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		p := filepath.Join(dst, filepath.FromSlash(name))
		if f.FileInfo().IsDir() {
			os.MkdirAll(p, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(p)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, rc)
		rc.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

var reALT = regexp.MustCompile(`(?i)^general \(ALT(\d*)\)\.bat$`)

func batRank(name string) int {
	switch {
	case strings.EqualFold(name, "general.bat"):
		return 0
	case reALT.MatchString(name):
		n, _ := strconv.Atoi(reALT.FindStringSubmatch(name)[1])
		if n == 0 {
			n = 1
		}
		return n
	case strings.Contains(strings.ToUpper(name), "EXP"):
		return 1000
	}
	return 100
}

// parse превращает general*.bat в аргументы winws: %BIN%/%LISTS% -> пути, игровой фильтр выключен (как в Flowseal
// по умолчанию), + --wf-iface (только реальный адаптер) и исключение сервера VPN в каждом профиле.
func (z *zapret) parse() error {
	bats, _ := filepath.Glob(filepath.Join(z.root, "general*.bat"))
	sort.Slice(bats, func(i, j int) bool {
		a, b := filepath.Base(bats[i]), filepath.Base(bats[j])
		if ra, rb := batRank(a), batRank(b); ra != rb {
			return ra < rb
		}
		return a < b
	})
	z.strategies = []zStrategy{{name: "none"}}
	bin := filepath.Join(z.root, "bin") + `\`
	lists := filepath.Join(z.root, "lists") + `\`
	for _, bat := range bats {
		raw, err := os.ReadFile(bat)
		if err != nil {
			continue
		}
		var cmd strings.Builder
		in := false
		for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
			t := strings.TrimSpace(line)
			if !in {
				i := strings.Index(t, `winws.exe"`)
				if i < 0 || !strings.HasPrefix(strings.ToLower(t), "start ") {
					continue
				}
				in = true
				t = t[i+len(`winws.exe"`):]
			}
			cont := strings.HasSuffix(t, "^")
			cmd.WriteString(strings.TrimSuffix(t, "^"))
			cmd.WriteString(" ")
			if !cont {
				break
			}
		}
		s := cmd.String()
		if strings.TrimSpace(s) == "" {
			continue
		}
		s = strings.NewReplacer("%BIN%", bin, "%LISTS%", lists, "%GameFilterTCP%", "12", "%GameFilterUDP%", "12",
			"%GameFilter%", "12").Replace(s)
		args := splitArgs(s)
		out := cleanZapretArgs(args, z.server)
		name := strings.TrimSuffix(filepath.Base(bat), ".bat")
		z.strategies = append(z.strategies, zStrategy{name: name, args: out})
	}
	if len(z.strategies) < 2 {
		return fmt.Errorf("в zapret нет стратегий")
	}
	return nil
}

// splitArgs — разбор командной строки батника: пробелы, кавычки (кавычки убираются).
func splitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	q, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			q = !q
			has = true
		case (r == ' ' || r == '\t') && !q:
			if has {
				args = append(args, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		args = append(args, cur.String())
	}
	return args
}

// cleanZapretArgs убирает из Flowseal general*.bat обход ГОЛОСА Discord (UDP 19294-19344/50000-50100, discord/stun):
// голос и прочий UDP идут через VPN (голосовые серверы Discord в РФ заблокированы по IP), а подмешанные winws
// fake-пакеты ломали бы WebRTC. TCP-часть Discord (сайт, API, CDN) остаётся — её обход проверяет и выбирает движок.
// В каждый профиль добавляется исключение сервера VPN.
func cleanZapretArgs(args []string, server string) []string {
	var blocks [][]string
	var curBlock []string
	for _, a := range args {
		if a == "--new" {
			if len(curBlock) > 0 {
				blocks = append(blocks, curBlock)
				curBlock = nil
			}
		} else {
			curBlock = append(curBlock, a)
		}
	}
	if len(curBlock) > 0 {
		blocks = append(blocks, curBlock)
	}

	var out []string
	for _, blk := range blocks {
		voice := false
		for j, a := range blk {
			low := strings.ToLower(a)
			switch {
			case strings.HasPrefix(low, "--filter-l7=") && (strings.Contains(low, "discord") || strings.Contains(low, "stun")):
				voice = true
			case strings.HasPrefix(low, "--filter-udp=") && (strings.Contains(low, "19294") || strings.Contains(low, "50000")):
				voice = true
			case strings.HasPrefix(low, "--wf-udp="):
				blk[j] = "--wf-udp=443"
			}
		}
		if voice {
			continue
		}
		if len(out) > 0 {
			out = append(out, "--new")
		}
		out = append(out, blk...)
		out = append(out, "--ipset-exclude-ip="+server)
	}
	return out
}

func (z *zapret) Outbounds() []map[string]any { return nil }

func (z *zapret) Strategies() []string {
	n := make([]string, len(z.strategies))
	for i, s := range z.strategies {
		n[i] = s.name
	}
	return n
}

func (z *zapret) setIface(idx uint32) {
	z.mu.Lock()
	changed := z.ifIndex != idx
	z.ifIndex = idx
	cur := z.cur
	z.mu.Unlock()
	if changed && cur > 0 {
		_, _ = z.Apply(context.Background(), cur)
	}
}

// Apply запускает winws со стратегией i (0 — без обхода). Весь обход — на выходе «direct».
func (z *zapret) Apply(ctx context.Context, i int) (string, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if i == z.cur && (i <= 0 || z.cmd != nil) && z.ifIndex != 0 {
		return "direct", nil
	}
	z.stopLocked()
	z.cur = -1
	if i < 0 || i >= len(z.strategies) {
		killForeignWinws(filepath.Join(z.root, "bin", "winws.exe"))
		return "", nil
	}
	if i == 0 {
		z.cur = 0
		return "direct", nil
	}
	killForeignWinws(filepath.Join(z.root, "bin", "winws.exe"))
	args := append([]string{fmt.Sprintf("--wf-iface=%d", z.ifIndex)}, z.strategies[i].args...)
	cmd := exec.Command(filepath.Join(z.root, "bin", "winws.exe"), args...)
	cmd.Dir = filepath.Join(z.root, "bin")
	cmd.SysProcAttr = hiddenProc()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("winws: %w", err)
	}
	z.job = killOnClose(cmd.Process.Pid)
	z.cmd = cmd
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		z.cmd = nil
		return "", fmt.Errorf("winws завершился: %v", err)
	case <-time.After(1500 * time.Millisecond):
	}
	go func(c *exec.Cmd) { // упал сам (антивирус, драйвер) — отметим, движок увидит по проверкам
		<-exited
		z.mu.Lock()
		if z.cmd == c {
			z.cmd = nil
			z.logf("winws exited")
		}
		z.mu.Unlock()
	}(cmd)
	z.cur = i
	z.logf("zapret: %s", z.strategies[i].name)
	return "direct", nil
}

func (z *zapret) stopLocked() {
	if z.cmd != nil && z.cmd.Process != nil {
		_ = z.cmd.Process.Kill()
	}
	z.cmd = nil
	if z.job != 0 {
		windows.CloseHandle(z.job)
		z.job = 0
	}
}

func (z *zapret) Stop() {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.stopLocked()
	killForeignWinws(filepath.Join(z.root, "bin", "winws.exe"))
	z.cur = -1
}

// ---------- процессы и службы

func hiddenProc() *windows.SysProcAttr {
	return &windows.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// killOnClose: процесс умрёт вместе с приложением, даже если его убьют (Job Object).
func killOnClose(pid int) windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, _ = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid)); err == nil {
		_ = windows.AssignProcessToJobObject(job, h)
		windows.CloseHandle(h)
	}
	return job
}

type procInfo struct {
	pid  uint32
	name string
	path string
}

func processes() []procInfo {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var out []procInfo
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		p := procInfo{pid: e.ProcessID, name: windows.UTF16ToString(e.ExeFile[:])}
		if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, e.ProcessID); err == nil {
			buf := make([]uint16, 1024)
			n := uint32(len(buf))
			if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil {
				p.path = windows.UTF16ToString(buf[:n])
			}
			windows.CloseHandle(h)
		}
		out = append(out, p)
	}
	return out
}

// чужой winws (zapret, запущенный батником) мешает нашему — два обхода на одном адаптере ломают трафик
func killForeignWinws(ours string) {
	for _, p := range processes() {
		if strings.EqualFold(p.name, "winws.exe") && !strings.EqualFold(p.path, ours) {
			if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, p.pid); err == nil {
				_ = windows.TerminateProcess(h, 1)
				windows.CloseHandle(h)
			}
		}
	}
}

// Службы обхода, которые мешают встроенному zapret: останавливаем на время подключения, потом возвращаем.
var conflictServices = []string{"zapret", "GoodbyeDPI", "winws1", "winws2"}

func stopConflictingServices() []string {
	m, err := mgr.Connect()
	if err != nil {
		return nil
	}
	defer m.Disconnect()
	var stopped []string
	for _, name := range conflictServices {
		s, err := m.OpenService(name)
		if err != nil {
			continue
		}
		if st, err := s.Query(); err == nil && st.State == svc.Running {
			if _, err := s.Control(svc.Stop); err == nil {
				stopped = append(stopped, name)
				for i := 0; i < 30; i++ {
					if st, err := s.Query(); err != nil || st.State == svc.Stopped {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
		}
		s.Close()
	}
	return stopped
}

func startServices(names []string) {
	if len(names) == 0 {
		return
	}
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	for _, name := range names {
		if s, err := m.OpenService(name); err == nil {
			_ = s.Start()
			s.Close()
		}
	}
}
