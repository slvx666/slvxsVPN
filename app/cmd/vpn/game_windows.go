//go:build windows

package main

import (
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Игровой режим (только на Wi-Fi): пока запущена игра, Windows не сканирует эфир в поисках сетей —
// фоновое сканирование даёт периодические «замирания» Wi-Fi и красные значки в играх.
// Проверено на ПК пользователя (wifi-probe): со сканированием выше скачки пинга.
var (
	gamePaths = regexp.MustCompile(`(?i)\\steamapps\\common\\|\\Epic Games\\|\\Riot Games\\|\\EA Games\\|\\Origin Games\\|\\Ubisoft Game Launcher\\games\\|\\GOG Galaxy\\Games\\|\\XboxGames\\|\\Games\\`)
	gameSkip  = regexp.MustCompile(`(?i)wallpaper|Steamworks Shared|redist|crash|launcher|EasyAntiCheat|BEService|unins|\\Steam\\steam|steamwebhelper|Riot Client|Battle\.net|EA Desktop|GalaxyClient|UbisoftConnect|\\upc\.exe|Overwolf|EpicOnlineServices|EpicWebHelper|UnrealCEFSubProcess|CefSharp|QtWebEngine`)
	gameNames = regexp.MustCompile(`(?i)^(r5apex|r5apex_dx12|cs2|dota2|VALORANT-Win64-Shipping|FortniteClient-Win64-Shipping|RainbowSix|TslGame|EscapeFromTarkov|GTA5|GTA5_Enhanced|RDR2|Overwatch|League of Legends|Warface|WorldOfTanks|WorldOfWarships|DeadByDaylight-Win64-Shipping|eldenring|Cyberpunk2077|Minecraft|javaw|FiveM|RobloxPlayerBeta|MarvelRivals-Win64-Shipping|deadlock|Rust|destiny2|bf6|bf2042)\.exe$`)
)

type gameMode struct {
	mu     sync.Mutex
	iface  string
	off    bool // сканирование выключено нами
	stop   chan struct{}
	logf   func(string, ...any)
	onFlip func(off bool, iface string)
	hold   time.Time // до этого момента сканирование не выключаем (сон/пробуждение: Wi-Fi должен спокойно подключиться)
}

func startGameMode(iface string, logf func(string, ...any), onFlip func(bool, string)) *gameMode {
	g := &gameMode{iface: iface, stop: make(chan struct{}), logf: logf, onFlip: onFlip}
	go g.loop()
	return g
}

func gameRunning() string {
	for _, p := range processes() {
		if (p.path != "" && gamePaths.MatchString(p.path) && !gameSkip.MatchString(p.path) && strings.HasSuffix(strings.ToLower(p.path), ".exe")) ||
			gameNames.MatchString(p.name) {
			return p.name
		}
	}
	return ""
}

func setWlanScan(iface string, on bool) error {
	v := "no"
	if on {
		v = "yes"
	}
	cmd := exec.Command("netsh", "wlan", "set", "autoconfig", "enabled="+v, "interface="+iface)
	cmd.SysProcAttr = hiddenProc()
	return cmd.Run()
}

func (g *gameMode) loop() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-g.stop:
			return
		case <-t.C:
		}
		game := gameRunning()
		g.mu.Lock()
		if time.Now().Before(g.hold) {
			game = "" // пауза: в этот момент автонастройка должна быть включена
		}
		if game != "" && !g.off {
			if setWlanScan(g.iface, false) == nil {
				g.off = true
				g.logf("game mode on: %s", game)
				g.onFlip(true, g.iface)
			}
		} else if game == "" && g.off {
			_ = setWlanScan(g.iface, true)
			g.off = false
			g.logf("game mode off")
			g.onFlip(false, g.iface)
		}
		g.mu.Unlock()
	}
}

// Release — перед сном/гибернацией: автонастройка Wi-Fi включается ВСЕГДА (иначе после пробуждения ПК не видит сети
// и не подключается), и на d режим не включается снова — после пробуждения Wi-Fi успевает подключиться.
func (g *gameMode) Release(d time.Duration) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hold = time.Now().Add(d)
	_ = setWlanScan(g.iface, true)
	if g.off {
		g.off = false
		g.logf("game mode off (сон)")
		g.onFlip(false, g.iface)
	}
}

func (g *gameMode) Stop() {
	if g == nil {
		return
	}
	close(g.stop)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.off {
		_ = setWlanScan(g.iface, true)
		g.off = false
		g.onFlip(false, g.iface)
	}
}

// Настройки питания Wi-Fi/USB (однократно для адаптера): на ПК пользователя это подняло скорость через VPN
// с 10 до 200 Мбит/с. Экономия энергии адаптера и USB — выкл., роуминг — минимум, фоновое сканирование драйвера — выкл.
const wifiTunePS = `
$ErrorActionPreference='SilentlyContinue'
foreach ($p in @(@('19cbb8fa-5279-450e-9fac-8a3d5fedd0c1','12bbebe6-58d6-4636-95bb-3217ef867c1a'),@('2a737441-1930-4402-8d77-b2bebba308a3','48e6b7a6-50f5-4782-a5d4-53bb8f07e226'))) {
  powercfg /setacvalueindex SCHEME_CURRENT $p[0] $p[1] 0 | Out-Null; powercfg /setdcvalueindex SCHEME_CURRENT $p[0] $p[1] 0 | Out-Null }
powercfg /setactive SCHEME_CURRENT | Out-Null
$a = Get-NetAdapter -Name '__IFACE__'
if ($a) {
  Set-NetAdapterPowerManagement -Name $a.Name -AllowComputerToTurnOffDevice Disabled -NoRestart
  $rules = @(
    @{ N='(?i)roam'; W='(?i)lowest|1\.\s|самый низк|минимал|^disable|откл' },
    @{ N='(?i)power ?sav|mimo power|energy|u-?apsd|энерго|экономи|smart ps'; W='(?i)^(disable|disabled|off|no smps|max(imum)? perf|откл|выкл)' },
    @{ N='(?i)packet coalescing|объединени'; W='(?i)^(disable|disabled|off|откл|выкл)' },
    @{ N='(?i)background scan|фонов'; W='(?i)^(disable|disabled|off|откл|выкл)' })
  foreach ($p in @(Get-NetAdapterAdvancedProperty -Name $a.Name)) { foreach ($r in $rules) {
    if ($p.DisplayName -notmatch $r.N) { continue }
    $v = @($p.ValidDisplayValues | Where-Object { $_ -match $r.W }) | Select-Object -First 1
    if ($v -and $v -ne $p.DisplayValue) { Set-NetAdapterAdvancedProperty -Name $a.Name -DisplayName $p.DisplayName -DisplayValue $v -NoRestart }
    break } }
  $cls='HKLM:\SYSTEM\CurrentControlSet\Control\Class\{4d36e972-e325-11ce-bfc1-08002be10318}'
  $k = Get-ChildItem $cls | Where-Object { (Get-ItemProperty $_.PSPath).NetCfgInstanceId -eq $a.InterfaceGuid } | Select-Object -First 1
  if ($k) { Set-ItemProperty -Path $k.PSPath -Name PnPCapabilities -Value 24 -Type DWord }
}
`

func tuneWiFi(iface string) {
	script := strings.ReplaceAll(wifiTunePS, "__IFACE__", strings.ReplaceAll(iface, "'", "''"))
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.SysProcAttr = hiddenProc()
	_ = cmd.Run()
}
