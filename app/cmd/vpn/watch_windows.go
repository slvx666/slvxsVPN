//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"vpnapp/cmd/vpn/wfp"
)

// Сторож (VPN.exe --watch <pid>): отдельный процесс следит за основным. Если основной упал (сбой, паника —
// не «Выход» из трея и не «Снять задачу»), сторож в ту же секунду ставит блокировку WFP (весь интернет,
// кроме самого приложения и локальной сети) и перезапускает VPN; блокировка действует, пока новый экземпляр
// не поднимет свою. Так сбой не превращается в утечку реального IP.
//
//	код завершения 0           — штатный выход (трей «Выход», «Отключить», выключение Windows): сторож уходит
//	код завершения 1           — процесс снят принудительно («Снять задачу»): воля пользователя, сторож уходит
//	любой другой (0xC0000005…) — авария: блокировка и перезапуск

func spawnWatcher() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "--watch", itoa(os.Getpid()))
	cmd.SysProcAttr = hiddenProc()
	if err := cmd.Start(); err != nil {
		logf("сторож: не запущен: %v", err)
		return
	}
	logf("сторож: запущен (pid %d)", cmd.Process.Pid)
	go cmd.Wait() // не оставлять zombie-дескриптор
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func watchMode(pid uint32) {
	dataDir := filepath.Join(os.Getenv("LOCALAPPDATA"), "VPN")
	lf, _ := os.OpenFile(filepath.Join(dataDir, "watch.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	log := func(s string) {
		if lf != nil {
			lf.WriteString(time.Now().Format("15:04:05 ") + s + "\n")
		}
	}
	exe, _ := os.Executable()
	for {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
		if err != nil {
			log("процесс уже завершён")
			return
		}
		windows.WaitForSingleObject(h, windows.INFINITE)
		var code uint32
		_ = windows.GetExitCodeProcess(h, &code)
		windows.CloseHandle(h)
		st := loadPersist(dataDir)
		log("основной процесс завершился, код " + itoa(int(code)))
		if st.ScanOffIface != "" { // игровой режим выключил автонастройку Wi-Fi — вернуть при любом выходе
			_ = setWlanScan(st.ScanOffIface, true)
			st.ScanOffIface = ""
			st.save(dataDir)
			log("автонастройка Wi-Fi возвращена")
		}
		if code == 0 || code == 1 || !st.WantOn {
			return
		}
		if os.Getenv("VPN_WATCH_DRY") != "" { // проверка логики без последствий (тесты)
			log("авария распознана (dry-run): блокировка и перезапуск пропущены")
			return
		}
		// авария при включённом VPN: сразу блокировка, потом перезапуск
		bridgeKillSwitch()
		log("авария: блокировка включена, перезапускаю VPN")
		cmd := exec.Command(exe, "--tray", "--watched")
		cmd.SysProcAttr = hiddenProc()
		if err := cmd.Start(); err != nil {
			log("перезапуск не удался: " + err.Error())
			return // блокировка остаётся, пока жив сторож; процесс завершится — снимется (задача: запустить VPN вручную)
		}
		pid = uint32(cmd.Process.Pid)
		go cmd.Wait()
		// своя блокировка нового экземпляра ставится при его старте; сторож снимает свою чуть позже (без «дыры»)
		time.AfterFunc(20*time.Second, func() { wfp.DisableFirewall() })
	}
}
