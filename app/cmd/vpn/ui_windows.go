//go:build windows

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")

	pRegisterClassExW       = user32.NewProc("RegisterClassExW")
	pCreateWindowExW        = user32.NewProc("CreateWindowExW")
	pDefWindowProcW         = user32.NewProc("DefWindowProcW")
	pShowWindow             = user32.NewProc("ShowWindow")
	pSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	pGetMessageW            = user32.NewProc("GetMessageW")
	pTranslateMessage       = user32.NewProc("TranslateMessage")
	pDispatchMessageW       = user32.NewProc("DispatchMessageW")
	pPostMessageW           = user32.NewProc("PostMessageW")
	pPostQuitMessage        = user32.NewProc("PostQuitMessage")
	pDestroyWindow          = user32.NewProc("DestroyWindow")
	pLoadImageW             = user32.NewProc("LoadImageW")
	pFindWindowW            = user32.NewProc("FindWindowW")
	pGetDpiForWindow        = user32.NewProc("GetDpiForWindow")
	pSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pAdjustWindowRectExForDpi = user32.NewProc("AdjustWindowRectExForDpi")
	pSetWindowPos           = user32.NewProc("SetWindowPos")
	pGetClientRect          = user32.NewProc("GetClientRect")
	pIsWindowVisible        = user32.NewProc("IsWindowVisible")
	pIsIconic               = user32.NewProc("IsIconic")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pOpenClipboard          = user32.NewProc("OpenClipboard")
	pCloseClipboard         = user32.NewProc("CloseClipboard")
	pGetClipboardData       = user32.NewProc("GetClipboardData")
	pMessageBoxW            = user32.NewProc("MessageBoxW")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	pShellExecuteW          = shell32.NewProc("ShellExecuteW")
	pDwmSetWindowAttribute  = dwmapi.NewProc("DwmSetWindowAttribute")
	pGlobalLock             = kernel32.NewProc("GlobalLock")
	pGlobalUnlock           = kernel32.NewProc("GlobalUnlock")
	pCreateSolidBrush       = gdi32.NewProc("CreateSolidBrush")
)

const (
	className   = "VPNAppWindow"
	wmTray      = 0x8000 + 1 // WM_APP+1
	wmShow      = 0x8000 + 2 // показать окно (второй запуск)
	wmDispatch  = 0x8000 + 3 // выполнить функции в потоке окна
	wmClose     = 0x0010
	wmDestroy   = 0x0002
	wmSize      = 0x0005
	wmMove      = 0x0003
	wmCommand   = 0x0111
	wmDpiChanged = 0x02E0
	wmLButtonUp = 0x0202
	wmRButtonUp = 0x0205
	wmLButtonDblClk = 0x0203
	wmActivate  = 0x0006
	wmPowerBroadcast = 0x0218
	nimAdd      = 0
	nimModify   = 1
	nimDelete   = 2
	nifMessage  = 1
	nifIcon     = 2
	nifTip      = 4
	idExit      = 100
	idOpen      = 101
	idToggle    = 102
)

type wndClassEx struct {
	Size, Style                        uint32
	WndProc                            uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background windows.Handle
	MenuName, ClassName                *uint16
	IconSm                             windows.Handle
}

type msgT struct {
	Hwnd    windows.Handle
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type notifyIconData struct {
	Size             uint32
	Wnd              windows.Handle
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             windows.Handle
	Tip              [128]uint16
	State, StateMask uint32
	Info             [256]uint16
	Version          uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             windows.GUID
	BalloonIcon      windows.Handle
}

type window struct {
	hwnd     windows.Handle
	web      *edge.Chromium
	icon     windows.Handle
	iconOff  windows.Handle
	tray     notifyIconData
	trayOn   bool
	mu       sync.Mutex
	queue    []func()
	onMsg    func(string)
	onClose  func() bool // true — спрятать в трей вместо выхода
	onTray   func(cmd int)
	taskbarCreated uint32
}

var mainWin *window

func utf16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

// Окно ~ 340x540 логических точек (как макет), тёмный заголовок, без изменения размера.
func newWindow(title string, show bool, onMsg func(string)) (*window, error) {
	runtime.LockOSThread()
	pSetProcessDpiAwarenessContext.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 (-4)
	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	w := &window{onMsg: onMsg}
	mainWin = w
	// rsrc встраивает: манифест = ID 1, группа иконки = ID 2 (см. сборку rsrc.syso)
	w.icon = loadIcon(hinst, 2)
	if w.icon == 0 {
		for id := uintptr(1); id <= 12 && w.icon == 0; id++ {
			w.icon = loadIcon(hinst, id)
		}
	}
	w.iconOff = w.icon
	brush, _, _ := pCreateSolidBrush.Call(0x111111)
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: hinst, Icon: w.icon, IconSm: w.icon,
		Background: windows.Handle(brush), ClassName: utf16(className)}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	r, _, _ := pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16("TaskbarCreated"))))
	w.taskbarCreated = uint32(r)

	// WS_CLIPCHILDREN обязателен: иначе окно закрашивает фоном дочернее окно WebView2 — чёрный экран
	const style = 0x00C00000 | 0x00080000 | 0x00020000 | 0x02000000 // WS_CAPTION|WS_SYSMENU|WS_MINIMIZEBOX|WS_CLIPCHILDREN
	h, _, err := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16(className))), uintptr(unsafe.Pointer(utf16(title))),
		style, 0x80000000, 0x80000000, 360, 560, 0, 0, uintptr(hinst), 0)
	if h == 0 {
		return nil, err
	}
	w.hwnd = windows.Handle(h)
	// тёмный заголовок (Windows 10 20H1+/11) и цвет рамки/заголовка как у приложения
	one := int32(1)
	pDwmSetWindowAttribute.Call(h, 20, uintptr(unsafe.Pointer(&one)), 4) // DWMWA_USE_IMMERSIVE_DARK_MODE
	col := uint32(0x00111111)
	pDwmSetWindowAttribute.Call(h, 35, uintptr(unsafe.Pointer(&col)), 4) // DWMWA_CAPTION_COLOR (Win11)
	pDwmSetWindowAttribute.Call(h, 34, uintptr(unsafe.Pointer(&col)), 4) // DWMWA_BORDER_COLOR
	txt := uint32(0x00EEEEEE)
	pDwmSetWindowAttribute.Call(h, 36, uintptr(unsafe.Pointer(&txt)), 4) // DWMWA_TEXT_COLOR
	w.fitSize()
	return w, nil
}

func loadIcon(hinst windows.Handle, id uintptr) windows.Handle {
	r, _, _ := pLoadImageW.Call(uintptr(hinst), id, 1 /*IMAGE_ICON*/, 0, 0, 0x8040 /*LR_DEFAULTSIZE|LR_SHARED*/)
	return windows.Handle(r)
}

func (w *window) dpi() uint32 {
	d, _, _ := pGetDpiForWindow.Call(uintptr(w.hwnd))
	if d == 0 {
		return 96
	}
	return uint32(d)
}

func (w *window) fitSize() {
	dpi := w.dpi()
	cw, ch := int32(340*dpi/96), int32(540*dpi/96)
	rc := struct{ L, T, R, B int32 }{0, 0, cw, ch}
	pAdjustWindowRectExForDpi.Call(uintptr(unsafe.Pointer(&rc)), 0x00C00000|0x00080000|0x00020000, 0, 0, uintptr(dpi))
	sw, _, _ := pGetSystemMetrics.Call(0)
	sh, _, _ := pGetSystemMetrics.Call(1)
	W, H := rc.R-rc.L, rc.B-rc.T
	pSetWindowPos.Call(uintptr(w.hwnd), 0, uintptr((int32(sw)-W)/2), uintptr((int32(sh)-H)/2), uintptr(W), uintptr(H), 0x0004|0x0010)
}

// embed запускает WebView2 внутри окна. Возвращает false, если WebView2 Runtime не установлен.
func (w *window) embed(dataPath, html, initJS string) bool {
	c := edge.NewChromium()
	c.DataPath = dataPath
	c.MessageCallback = func(m string) {
		if w.onMsg != nil {
			w.onMsg(m)
		}
	}
	c.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
		var rc struct{ L, T, R, B int32 }
		pGetClientRect.Call(uintptr(w.hwnd), uintptr(unsafe.Pointer(&rc)))
		logf("webview: страница загружена, клиент %dx%d", rc.R-rc.L, rc.B-rc.T)
	}
	c.SetPermission(edge.CoreWebView2PermissionKindClipboardRead, edge.CoreWebView2PermissionStateAllow)
	logf("webview: создание (данные %s)", dataPath)
	if !c.Embed(uintptr(w.hwnd)) {
		logf("webview: Embed не удался")
		return false
	}
	logf("webview: контроллер готов")
	w.web = c
	if ctl := c.GetController(); ctl != nil {
		if c2 := ctl.GetICoreWebView2Controller2(); c2 != nil {
			_ = c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: 0x11, G: 0x11, B: 0x11})
		}
	}
	if s, err := c.GetSettings(); err == nil {
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutIsZoomControlEnabled(false)
		_ = s.PutAreBrowserAcceleratorKeysEnabled(false)
		_ = s.PutIsPinchZoomEnabled(false)
		_ = s.PutIsSwipeNavigationEnabled(false)
	}
	c.Resize()
	if initJS != "" {
		c.Init(initJS)
	}
	// грузим через файл file:// — надёжнее, чем NavigateToString (у последнего в WebView2
	// бывает пустая/чёрная страница). Файл кладём рядом с данными.
	htmlPath := filepath.Join(filepath.Dir(dataPath), "index.html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err == nil {
		c.Navigate("file:///" + strings.ReplaceAll(htmlPath, "\\", "/"))
	} else {
		c.NavigateToString(html)
	}
	return true
}

func (w *window) show() {
	pShowWindow.Call(uintptr(w.hwnd), 9) // SW_RESTORE
	pShowWindow.Call(uintptr(w.hwnd), 5) // SW_SHOW
	pSetForegroundWindow.Call(uintptr(w.hwnd))
	if w.web != nil {
		// контроллер создаётся, пока окно скрыто, — тогда WebView2 остаётся невидимым (чёрный экран)
		if ctl := w.web.GetController(); ctl != nil {
			_ = ctl.PutIsVisible(true)
		}
		w.web.Resize()
		w.web.Focus()
	}
}

func (w *window) hide() {
	if w.web != nil {
		if ctl := w.web.GetController(); ctl != nil {
			_ = ctl.PutIsVisible(false) // в трее не рисуем
		}
	}
	pShowWindow.Call(uintptr(w.hwnd), 0)
}

func (w *window) visible() bool {
	r, _, _ := pIsWindowVisible.Call(uintptr(w.hwnd))
	i, _, _ := pIsIconic.Call(uintptr(w.hwnd))
	return r != 0 && i == 0
}

// dispatch — выполнить f в потоке окна (WebView2 работает только из него).
func (w *window) dispatch(f func()) {
	w.mu.Lock()
	w.queue = append(w.queue, f)
	w.mu.Unlock()
	pPostMessageW.Call(uintptr(w.hwnd), wmDispatch, 0, 0)
}

func (w *window) eval(js string) {
	w.dispatch(func() {
		if w.web != nil {
			w.web.Eval(js)
		}
	})
}

func (w *window) run() {
	var m msgT
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (w *window) quit() {
	w.trayRemove()
	pDestroyWindow.Call(uintptr(w.hwnd))
}

// ---------- значок в трее

func (w *window) traySet(on bool, tip string) {
	w.tray.Size = uint32(unsafe.Sizeof(w.tray))
	w.tray.Wnd = w.hwnd
	w.tray.ID = 1
	w.tray.Flags = nifMessage | nifIcon | nifTip
	w.tray.CallbackMessage = wmTray
	w.tray.Icon = w.iconOff
	if on {
		w.tray.Icon = w.icon
	}
	t, _ := windows.UTF16FromString(tip)
	copy(w.tray.Tip[:len(w.tray.Tip)-1], t)
	if len(t) < len(w.tray.Tip) {
		w.tray.Tip[len(t)] = 0
	}
	op := uintptr(nimModify)
	if !w.trayOn {
		op = nimAdd
	}
	r, _, _ := pShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&w.tray)))
	if r != 0 {
		w.trayOn = true
	}
}

func (w *window) trayRemove() {
	if w.trayOn {
		pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&w.tray)))
		w.trayOn = false
	}
}

func (w *window) trayMenu(connected bool) {
	m, _, _ := pCreatePopupMenu.Call()
	pAppendMenuW.Call(m, 0, idOpen, uintptr(unsafe.Pointer(utf16("Открыть"))))
	label := "Подключить"
	if connected {
		label = "Отключить"
	}
	pAppendMenuW.Call(m, 0, idToggle, uintptr(unsafe.Pointer(utf16(label))))
	pAppendMenuW.Call(m, 0x800, 0, 0) // separator
	pAppendMenuW.Call(m, 0, idExit, uintptr(unsafe.Pointer(utf16("Выход"))))
	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(uintptr(w.hwnd))
	cmd, _, _ := pTrackPopupMenu.Call(m, 0x0100|0x0020 /*RETURNCMD|BOTTOMALIGN*/, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(w.hwnd), 0)
	pDestroyMenu.Call(m)
	if cmd != 0 && w.onTray != nil {
		w.onTray(int(cmd))
	}
}

func wndProc(hwnd windows.Handle, msg uint32, wp, lp uintptr) uintptr {
	w := mainWin
	if w == nil || w.hwnd == 0 {
		r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wp, lp)
		return r
	}
	switch msg {
	case wmDispatch:
		w.mu.Lock()
		q := w.queue
		w.queue = nil
		w.mu.Unlock()
		for _, f := range q {
			f()
		}
		return 0
	case wmShow:
		w.show()
		return 0
	case wmTray:
		switch uint32(lp) & 0xFFFF {
		case wmLButtonUp, wmLButtonDblClk:
			w.show()
		case wmRButtonUp:
			w.trayMenu(app != nil && app.connectedOrConnecting())
		}
		return 0
	case wmClose:
		if w.onClose != nil && w.onClose() {
			w.hide()
			return 0
		}
		w.quit()
		return 0
	case wmPowerBroadcast:
		// PBT_APMSUSPEND (4): ПК засыпает — успеть вернуть автонастройку Wi-Fi (до сна есть ~2 с)
		if wp == 4 && app != nil {
			app.onSuspend()
		}
		// PBT_APMRESUMESUSPEND (7) / PBT_APMRESUMEAUTOMATIC (0x12): ПК проснулся (сон/гибернация)
		if (wp == 7 || wp == 0x12) && app != nil {
			go app.onResume()
		}
		return 1
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	case wmSize:
		if w.web != nil {
			w.web.Resize()
		}
	case wmMove:
		if w.web != nil {
			_ = w.web.NotifyParentWindowPositionChanged()
		}
	case wmDpiChanged:
		w.fitSize()
		return 0
	case wmActivate:
		if wp&0xFFFF != 0 && w.web != nil {
			w.web.Focus()
		}
	}
	if w.taskbarCreated != 0 && msg == w.taskbarCreated { // Explorer перезапустился — вернуть значок
		w.trayOn = false
		if app != nil {
			app.updateTray()
		}
	}
	r, _, _ := pDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wp, lp)
	return r
}

// ---------- мелочи

func clipboardText() string {
	r, _, _ := pOpenClipboard.Call(0)
	if r == 0 {
		return ""
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(13) // CF_UNICODETEXT
	if h == 0 {
		return ""
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(p)))
}

func messageBox(text string, yesNo bool) bool {
	flags := uintptr(0x40) // MB_ICONINFORMATION
	if yesNo {
		flags = 0x04 | 0x20 // MB_YESNO|MB_ICONQUESTION
	}
	r, _, _ := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(utf16(text))), uintptr(unsafe.Pointer(utf16("VPN"))), flags)
	return r == 1 || r == 6
}

func openURL(u string) {
	pShellExecuteW.Call(0, uintptr(unsafe.Pointer(utf16("open"))), uintptr(unsafe.Pointer(utf16(u))), 0, 0, 1)
}

func findExisting() windows.Handle {
	r, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(utf16(className))), 0)
	return windows.Handle(r)
}

func postShow(h windows.Handle) { pPostMessageW.Call(uintptr(h), wmShow, 0, 0) }

var pGetWindowThreadProcessId = user32.NewProc("GetWindowThreadProcessId")

// replaceOther: если окно h принадлежит процессу из другого exe — завершить его (WM_DESTROY → штатный
// выход с отключением VPN, через 5 с — принудительно). true — старый закрыт, можно запускаться.
func replaceOther(h windows.Handle) bool {
	var pid uint32
	pGetWindowThreadProcessId.Call(uintptr(h), uintptr(unsafe.Pointer(&pid)))
	if pid == 0 {
		return false
	}
	ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(ph)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if windows.QueryFullProcessImageName(ph, 0, &buf[0], &n) != nil {
		return false
	}
	other := windows.UTF16ToString(buf[:n])
	self, _ := os.Executable()
	if strings.EqualFold(filepath.Clean(other), filepath.Clean(self)) {
		return false
	}
	pPostMessageW.Call(uintptr(h), wmDestroy, 0, 0)
	if ev, _ := windows.WaitForSingleObject(ph, 5000); ev != windows.WAIT_OBJECT_0 {
		windows.TerminateProcess(ph, 0)
		windows.WaitForSingleObject(ph, 2000)
	}
	return true
}

var _ = syscall.Getpid
