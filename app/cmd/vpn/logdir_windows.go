//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"vpnapp/core"
)

// Журнал в папке пользователя (кнопка «Журнал диагностики» в окне): к app.log добавляется vpn-log.txt в выбранной
// папке — с датой, событиями ядра (круги проверок, смены протоколов и сети) и снимком состояния раз в минуту.

var (
	sinkMu   sync.Mutex
	sinkFile *os.File
	sinkDir  string

	pSHBrowseForFolderW  = shell32.NewProc("SHBrowseForFolderW")
	pSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	pCoTaskMemFree       = ole32.NewProc("CoTaskMemFree")
)

type browseInfo struct {
	Owner       windows.Handle
	Root        uintptr
	DisplayName *uint16
	Title       *uint16
	Flags       uint32
	Fn          uintptr
	LParam      uintptr
	Image       int32
}

// pickFolder — системный диалог выбора папки (вызывать из потока окна).
func pickFolder(owner windows.Handle, title string) string {
	name := make([]uint16, windows.MAX_PATH)
	bi := browseInfo{Owner: owner, DisplayName: &name[0], Title: windows.StringToUTF16Ptr(title),
		Flags: 0x0001 | 0x0040 | 0x0010} // только папки файловой системы | новый стиль | поле ввода
	pidl, _, _ := pSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer pCoTaskMemFree.Call(pidl)
	path := make([]uint16, windows.MAX_PATH)
	if r, _, _ := pSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&path[0]))); r == 0 {
		return ""
	}
	return windows.UTF16ToString(path)
}

// setLogDir открывает (или закрывает, если dir пусто) файл журнала в выбранной папке.
func setLogDir(dir string) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	if sinkFile != nil {
		sinkFile.Close()
		sinkFile = nil
	}
	sinkDir = ""
	if dir == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "vpn-log.txt"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	if st, err := f.Stat(); err == nil && st.Size() > 10<<20 {
		f.Truncate(0)
	}
	sinkFile, sinkDir = f, dir
	fmt.Fprintf(f, "%s === журнал VPN %s | Windows ===\n", time.Now().Format("2006-01-02 15:04:05"), core.Version)
}

func sinkWrite(line string) {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	if sinkFile != nil {
		fmt.Fprintf(sinkFile, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
	}
}

func logDirName() string {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	if sinkDir == "" {
		return ""
	}
	return filepath.Base(sinkDir)
}
