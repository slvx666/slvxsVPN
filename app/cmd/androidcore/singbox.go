package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"vpnapp/bdpi"
	"vpnapp/core"
)

func buildSingboxConfig(dataDir string, p *core.Profile) map[string]any {
	outbounds := []map[string]any{
		{
			"type":      "urltest",
			"tag":       "auto",
			"outbounds": []string{"hysteria2", "vless-reality"},
			"url":       "https://www.gstatic.com/generate_204",
			"interval":  "1m",
			"tolerance": 50,
		},
		{
			"type":        "hysteria2",
			"tag":         "hysteria2",
			"server":      p.Server,
			"server_port": p.Port,
			"password":    p.UUID,
			"obfs": map[string]any{
				"type":     "salamander",
				"password": p.Hy2.Obfs,
			},
			"tls": map[string]any{
				"enabled":     true,
				"server_name": p.Server,
				"insecure":    true,
			},
		},
		{
			"type":        "vless",
			"tag":         "vless-reality",
			"server":      p.Server,
			"server_port": p.Port,
			"uuid":        p.UUID,
			"flow":        "xtls-rprx-vision",
			"tls": map[string]any{
				"enabled":     true,
				"server_name": p.SNI,
				"utls": map[string]any{
					"enabled":     true,
					"fingerprint": "chrome",
				},
				"reality": map[string]any{
					"enabled":    true,
					"public_key": p.PublicKey,
					"short_id":   p.ShortID,
				},
			},
		},
		{
			"type":            "socks",
			"tag":             "byedpi",
			"server":          "127.0.0.1",
			"server_port":     10801,
			"domain_strategy": "prefer_ipv4",
		},
		{
			"type": "direct",
			"tag":  "direct",
		},
	}

	rules := []map[string]any{
		{
			"protocol": "dns",
			"action":   "hijack-dns",
		},
		// YouTube через ByeDPI с российского IP (без рекламы, 1080p/4K с локальных CDN)
		{
			"domain_suffix": []string{
				"googlevideo.com", "youtube.com", "ytimg.com", "youtu.be", "ggpht.com", "gvt1.com", "youtubei.googleapis.com",
			},
			"outbound": "byedpi",
		},
		// Блокируем QUIC (UDP 443) — чтобы YouTube и браузеры мгновенно переходили на
		// высокоскоростной TCP HTTP/2 (без задержек и троттлинга операторов)
		{
			"network": "udp",
			"port":    []int{443},
			"action":  "reject",
		},
		// Российские ресурсы — напрямую с родного IP провайдера на максимальной скорости
		{
			"domain_suffix": []string{
				".ru", ".su", ".xn--p1ai", "vk.com", "yandex.ru", "ya.ru",
				"gosuslugi.ru", "sberbank.ru", "tinkoff.ru", "t-bank.ru", "alfabank.ru",
				"avito.ru", "ozon.ru", "wildberries.ru", "kinopoisk.ru", "rutube.ru",
			},
			"outbound": "direct",
		},
		// Интерактивный UDP (игры, звонки Telegram, voice) — через Hysteria2 без блокировок
		{
			"network":  "udp",
			"outbound": "hysteria2",
		},
	}

	if _, err := os.Stat(filepath.Join(dataDir, "geosite.db")); err == nil {
		rules = append(rules, map[string]any{
			"geosite":  []string{"category-ru"},
			"outbound": "direct",
		})
	}
	if _, err := os.Stat(filepath.Join(dataDir, "geoip.db")); err == nil {
		rules = append(rules, map[string]any{
			"geoip":    []string{"ru"},
			"outbound": "direct",
		})
	}

	return map[string]any{
		"log": map[string]any{
			"level":     "warn",
			"timestamp": true,
		},
		"dns": map[string]any{
			"servers": []map[string]any{
				{
					"tag":              "dns-remote",
					"address":          "https://1.1.1.1/dns-query",
					"address_resolver": "dns-direct",
					"strategy":         "ipv4_only",
					"detour":           "auto",
				},
				{
					"tag":      "dns-direct",
					"address":  "77.88.8.8",
					"strategy": "ipv4_only",
					"detour":   "direct",
				},
				{
					"tag":     "dns-block",
					"address": "rcode://success",
				},
			},
			"rules": []map[string]any{
				// Мгновенный ответ без задержек на IPv6 (AAAA) запросы
				{
					"query_type": []string{"AAAA"},
					"server":     "dns-block",
				},
				{
					"domain_suffix": []string{
						"googlevideo.com", "youtube.com", "ytimg.com", "youtu.be", "ggpht.com", "gvt1.com", "youtubei.googleapis.com",
						".ru", ".su", ".xn--p1ai", "vk.com", "yandex.ru", "ya.ru",
						"gosuslugi.ru", "sberbank.ru", "tinkoff.ru", "t-bank.ru", "alfabank.ru",
						"avito.ru", "ozon.ru", "wildberries.ru", "kinopoisk.ru", "rutube.ru",
					},
					"server": "dns-direct",
				},
			},
			"strategy": "ipv4_only",
		},
		"inbounds": []map[string]any{
			{
				"type":                       "tun",
				"tag":                        "tun-in",
				"interface_name":             "tun0",
				"mtu":                        1280,
				"address":                    []string{"172.19.0.1/30"},
				"auto_route":                 false,
				"strict_route":               false,
				"stack":                      "gvisor",
				"sniff":                      true,
				"sniff_override_destination": false,
			},
			{
				"type":        "mixed",
				"tag":         "mixed-in",
				"listen":      "127.0.0.1",
				"listen_port": 2080,
			},
		},
		"outbounds": outbounds,
		"route": map[string]any{
			"auto_detect_interface": false,
			"rules":                 rules,
			"final":                 "auto",
		},
	}
}

type singboxRunner struct {
	cmd      *exec.Cmd
	bp       *bdpi.B
	p        *core.Profile
	logf     func(string, ...any)
	done     chan struct{}
	netCh    chan struct{}
	stopOnce sync.Once
}

func probeLatency(proxyAddr, targetURL string, timeout time.Duration) (time.Duration, error) {
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		return 0, err
	}
	tr := &http.Transport{
		Proxy:             http.ProxyURL(proxyURL),
		DisableKeepAlives: true,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}
	t0 := time.Now()
	resp, err := client.Get(targetURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return 0, fmt.Errorf("http status %d", resp.StatusCode)
	}
	return time.Since(t0), nil
}

func startSingbox(dataDir, libDir string, tunFd int, p *core.Profile, logf func(string, ...any)) (*singboxRunner, error) {
	_ = syscall.SetNonblock(tunFd, true)

	// 1. ByeDPI для обхода замедлений YouTube/Discord/Twitch с родного IP (стратегия bd-q5-sack)
	bp := bdpi.New(filepath.Join(libDir, "libbyedpi.so"), 10801, logf)
	if _, err := bp.Apply(context.Background(), 2); err != nil {
		logf("start byedpi warning: %v", err)
	}

	// 2. Конфиг sing-box
	cfgPath := filepath.Join(dataDir, "sing-box.json")
	cfg := buildSingboxConfig(dataDir, p)
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		bp.Stop()
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(cfgPath, b, 0o644); err != nil {
		bp.Stop()
		return nil, fmt.Errorf("write config: %w", err)
	}

	// 3. Запуск sing-box
	singboxBin := filepath.Join(libDir, "libsingbox.so")
	cmd := exec.Command(singboxBin, "run", "-c", cfgPath, "-D", dataDir)
	cmd.ExtraFiles = []*os.File{os.NewFile(uintptr(tunFd), "tun")} // FD 3
	cmd.Env = append(os.Environ(), "SING_BOX_TUN_FD=3")

	logPath := filepath.Join(dataDir, "singbox.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		if diagDir := os.Getenv("VPN_DIAG_DIR"); diagDir != "" {
			diagPath := filepath.Join(diagDir, "singbox.log")
			diagFile, dErr := os.OpenFile(diagPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if dErr == nil {
				cmd.Stdout = io.MultiWriter(logFile, diagFile)
				cmd.Stderr = io.MultiWriter(logFile, diagFile)
			} else {
				cmd.Stdout = logFile
				cmd.Stderr = logFile
			}
		} else {
			cmd.Stdout = logFile
			cmd.Stderr = logFile
		}
	}

	if err := cmd.Start(); err != nil {
		bp.Stop()
		return nil, fmt.Errorf("start singbox: %w", err)
	}

	r := &singboxRunner{
		cmd:   cmd,
		bp:    bp,
		p:     p,
		logf:  logf,
		done:  make(chan struct{}),
		netCh: make(chan struct{}, 1),
	}

	// 3. Первичная проверка связи
	emit(core.State{State: "connecting", Detail: "Подключение к серверу...", TCP: "vless", UDP: "hy2"})

	initialOK := false
	var initialRTT time.Duration
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			r.stop()
			return nil, fmt.Errorf("sing-box процесс завершился")
		}
		rtt, err := probeLatency("127.0.0.1:2080", "https://www.gstatic.com/generate_204", 2500*time.Millisecond)
		if err == nil {
			initialOK = true
			initialRTT = rtt
			break
		}
		time.Sleep(300 * time.Millisecond)
	}

	if !initialOK {
		r.stop()
		emit(map[string]any{"state": "off", "error": "Нет связи с сервером. Проверьте интернет"})
		return nil, fmt.Errorf("initial connectivity probe failed")
	}

	ms := initialRTT.Milliseconds()
	country := p.Country
	if country == "" {
		country = "Германия"
	}
	emit(core.State{
		State:  "on",
		Detail: fmt.Sprintf("%s · %d мс", country, ms),
		TCP:    "vless",
		UDP:    "hy2",
	})

	go r.loop()
	return r, nil
}

func (r *singboxRunner) onNetworkChange() {
	select {
	case r.netCh <- struct{}{}:
	default:
	}
}

func (r *singboxRunner) loop() {
	ticker := time.NewTicker(12 * time.Second)
	defer ticker.Stop()

	failCount := 0
	country := r.p.Country
	if country == "" {
		country = "Германия"
	}

	for {
		select {
		case <-r.done:
			return
		case <-r.netCh:
			r.logf("network changed, probing latency...")
			time.Sleep(500 * time.Millisecond)
			rtt, err := probeLatency("127.0.0.1:2080", "https://www.gstatic.com/generate_204", 3*time.Second)
			if err == nil {
				failCount = 0
				emit(core.State{
					State:  "on",
					Detail: fmt.Sprintf("%s · %d мс", country, rtt.Milliseconds()),
					TCP:    "vless",
					UDP:    "hy2",
				})
			} else {
				emit(core.State{
					State:  "connecting",
					Detail: "Переподключение...",
					TCP:    "vless",
					UDP:    "hy2",
				})
			}
		case <-ticker.C:
			rtt, err := probeLatency("127.0.0.1:2080", "https://www.gstatic.com/generate_204", 3500*time.Millisecond)
			if err == nil {
				failCount = 0
				emit(core.State{
					State:  "on",
					Detail: fmt.Sprintf("%s · %d мс", country, rtt.Milliseconds()),
					TCP:    "vless",
					UDP:    "hy2",
				})
			} else {
				failCount++
				r.logf("probe failed (%d): %v", failCount, err)
				if failCount >= 2 {
					emit(core.State{
						State:  "connecting",
						Detail: "Переподключение...",
						TCP:    "vless",
						UDP:    "hy2",
					})
				}
			}
		}
	}
}

func (r *singboxRunner) stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.done)
		if r.cmd != nil && r.cmd.Process != nil {
			r.cmd.Process.Kill()
			r.cmd.Wait()
		}
		if r.bp != nil {
			r.bp.Stop()
		}
	})
}
