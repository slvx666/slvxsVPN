package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"vpnapp/bdpi"
	"vpnapp/core"
)

func buildSingboxConfig(dataDir string, p *core.Profile) map[string]any {
	outbounds := []map[string]any{
		{
			"type":      "urltest",
			"tag":       "auto",
			"outbounds": []string{"vless-reality", "hysteria2"},
			"url":       "https://www.gstatic.com/generate_204",
			"interval":  "1m",
			"tolerance": 50,
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
			"type":        "socks",
			"tag":         "byedpi",
			"server":      "127.0.0.1",
			"server_port": 10801,
		},
		{
			"type": "direct",
			"tag":  "direct",
		},
	}

	rules := []map[string]any{
		{
			"domain_suffix": []string{
				"googlevideo.com", "youtube.com", "ytimg.com", "youtu.be", "ggpht.com", "gvt1.com",
				"discord.com", "discord.gg", "discordapp.com", "discordapp.net",
				"twitch.tv", "ttvnw.net", "jtvnw.net",
			},
			"outbound": "byedpi",
		},
		{
			"domain_suffix": []string{
				".ru", ".su", ".xn--p1ai", "vk.com", "yandex.ru", "ya.ru",
				"gosuslugi.ru", "sberbank.ru", "tinkoff.ru", "t-bank.ru", "alfabank.ru",
			},
			"outbound": "direct",
		},
	}

	// Если базы geosite/geoip доступны в dataDir, добавляем расширенные правила
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
			"level":     "info",
			"timestamp": true,
		},
		"inbounds": []map[string]any{
			{
				"type":           "tun",
				"tag":            "tun-in",
				"interface_name": "tun0",
				"mtu":            1420,
				"address":        []string{"172.19.0.1/30"},
				"auto_route":     false,
				"strict_route":   false,
				"stack":          "system",
				"sniff":          true,
			},
		},
		"outbounds": outbounds,
		"route": map[string]any{
			"auto_detect_interface": true,
			"rules":                 rules,
			"final":                 "auto",
		},
	}
}

type singboxRunner struct {
	cmd *exec.Cmd
	bp  *bdpi.B
}

func startSingbox(dataDir, libDir string, tunFd int, p *core.Profile, logf func(string, ...any)) (*singboxRunner, error) {
	// 1. ByeDPI для обхода замедлений YouTube/Discord/Twitch с русского IP (стратегия bd-q5-sack)
	bp := bdpi.New(filepath.Join(libDir, "libbyedpi.so"), 10801, logf)
	if _, err := bp.Apply(context.Background(), 2); err != nil {
		logf("start byedpi warning: %v", err)
	}

	// 2. Генерация конфига sing-box
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

	// 3. Запуск sing-box с передачей дескриптора TUN (SING_BOX_TUN_FD=3)
	singboxBin := filepath.Join(libDir, "libsingbox.so")
	cmd := exec.Command(singboxBin, "run", "-c", cfgPath, "-D", dataDir)
	cmd.ExtraFiles = []*os.File{os.NewFile(uintptr(tunFd), "tun")} // fd 3 в дочернем процессе
	cmd.Env = append(os.Environ(), "SING_BOX_TUN_FD=3")

	logFile, err := os.OpenFile(filepath.Join(dataDir, "singbox.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		bp.Stop()
		return nil, fmt.Errorf("start singbox: %w", err)
	}

	return &singboxRunner{cmd: cmd, bp: bp}, nil
}

func (r *singboxRunner) stop() {
	if r == nil {
		return
	}
	if r.cmd != nil && r.cmd.Process != nil {
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}
	if r.bp != nil {
		r.bp.Stop()
	}
}
