//go:build windows

package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// persist — что нужно вернуть системе при выходе (мы меняли на время подключения).
type persist struct {
	PrevSmartDNS int    `json:"prev_smart_dns"` // -2 не трогали, -1 значения не было, >=0 прежнее
	WantOn       bool   `json:"want_on"`        // был ли включён VPN — для автозапуска
	LastSub      string `json:"last_sub"`
	LogDir       string `json:"log_dir,omitempty"`        // папка журнала диагностики (выбирает пользователь)
	ScanOffIface string `json:"scan_off_iface,omitempty"` // игровой режим выключил автонастройку Wi-Fi на этом адаптере
}

func loadPersist(dir string) *persist {
	p := &persist{PrevSmartDNS: -2}
	if b, err := os.ReadFile(filepath.Join(dir, "state.json")); err == nil {
		_ = json.Unmarshal(b, p)
	}
	return p
}

func (p *persist) save(dir string) {
	b, _ := json.MarshalIndent(p, "", " ")
	tmp := filepath.Join(dir, "state.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, filepath.Join(dir, "state.json"))
	}
}

// goIfaceName — имя адаптера в терминах Go net (для sockopt interface = привязка исходящих Xray к реальному NIC).
func goIfaceName(index uint32) string {
	if ifs, err := net.Interfaces(); err == nil {
		for _, i := range ifs {
			if i.Index == int(index) {
				return i.Name
			}
		}
	}
	return ""
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// setAutostart — запуск в трее при входе в систему (VPN сам поднимется, если был включён).
func setAutostart(on bool) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if on {
		if exe, err := os.Executable(); err == nil {
			_ = k.SetStringValue("SewrGate", `"`+exe+`" --tray`)
			_ = k.DeleteValue("VPN")
		}
	} else {
		_ = k.DeleteValue("SewrGate")
		_ = k.DeleteValue("VPN")
	}
}
