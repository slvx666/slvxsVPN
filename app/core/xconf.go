package core

import (
	"strings"
)

// Адреса внутри туннеля (одинаковые для ПК и Android).
const (
	TunAddr4 = "172.19.0.1"
	TunDNS4  = "172.19.0.2" // «DNS-сервер» внутри туннеля: запросы к нему перехватывает Xray (fakedns)
	TunAddr6 = "fdfe:dcba:9876::1"
	TunMTU   = 1280
)

// Теги выходов Xray. sw-* — переключатели (switch.go): куда они ведут, решает движок по результатам проверок.
const (
	tagVLESS  = "vless"
	tagXHTTP  = "xhttp"
	tagHy2    = "hy2"
	tagDirect = "direct"
	tagBlock  = "block"
	tagDNS    = "dns-out"
	swTCP     = "sw-tcp"
	swUDP     = "sw-udp"
)

func swService(id string) string { return "sw-" + id }

type obj = map[string]any

func (e *Engine) buildConfig() obj {
	p := e.profile
	// БЕЗ tcpFastOpen: провайдер (СПб, 2026-09) режет TCP с данными в SYN.
	// tcpKeepAlive: 15 сек — исключает сброс сессий в NAT роутеров и мобильных операторов (CGNAT).
	sockopt := obj{
		"tcpKeepAliveIdle":     15,
		"tcpKeepAliveInterval": 15,
	}
	if e.opt.Platform != "android" && e.opt.BindIface != "" {
		sockopt["interface"] = e.opt.BindIface
	}
	reality := obj{"serverName": p.SNI, "fingerprint": "chrome", "publicKey": p.PublicKey, "shortId": p.ShortID, "spiderX": "/"}

	hy2mask := obj{"udp": []obj{{"type": "salamander", "settings": obj{"password": p.Hy2.Obfs}}}}
	qp := obj{
		"congestion":                  "bbr",
		"maxIdleTimeout":              20,
		"keepAlivePeriod":             8,
		"initStreamReceiveWindow":     8388608,
		"maxStreamReceiveWindow":      16777216,
		"initConnReceiveWindow":       16777216,
		"maxConnReceiveWindow":        33554432,
		"disablePathMTUDiscovery":     true,
	}
	if p.Hy2.Hop != "" {
		// прыжки портов: провайдер не видит один «вечный» UDP-поток; клиент Xray прыгает без потерь
		qp["udpHop"] = obj{"ports": p.Hy2.Hop, "interval": "30-60"}
	}
	hy2mask["quicParams"] = qp
	hy2sni := p.Hy2.SNI
	if hy2sni == "" {
		hy2sni = p.Server
	}
	hy2tls := obj{"serverName": hy2sni, "alpn": []string{"h3"}}
	pin := p.Hy2.PinSHA256
	if pin == "" {
		pin = "865de47fb55fddb470e917d6cee77b38417194e16e1c03b94d7649469324519b"
	}
	hy2tls["pinnedPeerCertSha256"] = pin

	outbounds := []obj{
		{"tag": tagVLESS, "protocol": "vless",
			"settings": obj{"vnext": []obj{{"address": p.Server, "port": p.Port, "users": []obj{{"id": p.UUID, "encryption": "none", "flow": "xtls-rprx-vision"}}}}},
			"streamSettings": obj{"network": "tcp", "security": "reality", "realitySettings": reality, "sockopt": sockopt}},
		{"tag": tagXHTTP, "protocol": "vless",
			"settings": obj{"vnext": []obj{{"address": p.Server, "port": p.Port, "users": []obj{{"id": p.UUID, "encryption": "none"}}}}},
			"streamSettings": obj{"network": "xhttp", "xhttpSettings": obj{"path": p.XHTTPPath, "mode": "auto"},
				"security": "reality", "realitySettings": reality, "sockopt": sockopt}},
		{"tag": tagHy2, "protocol": "hysteria",
			"settings": obj{"version": 2, "address": p.Server, "port": p.Port},
			"streamSettings": obj{"network": "hysteria", "security": "tls",
				"tlsSettings":      hy2tls,
				"hysteriaSettings": obj{"version": 2, "auth": p.UUID},
				"finalmask":        hy2mask, "sockopt": sockopt}},
		{"tag": tagDirect, "protocol": "freedom", "settings": obj{"domainStrategy": "UseIPv4"},
			"streamSettings": obj{"sockopt": sockopt}},
		{"tag": tagBlock, "protocol": "blackhole"},
		{"tag": tagDNS, "protocol": "dns", "settings": obj{"nonIPQuery": "reject"}},
	}
	if e.opt.Bypass != nil {
		for _, o := range e.opt.Bypass.Outbounds() {
			if ss, ok := o["streamSettings"].(obj); ok && e.opt.BindIface != "" {
				if so, ok := ss["sockopt"].(obj); ok {
					so["interface"] = e.opt.BindIface
				}
			}
			outbounds = append(outbounds, o)
		}
	}

	// локальные сети (без 198.18.0.0/15 — это пул fakedns)
	private := []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.168.0.0/16", "224.0.0.0/4", "255.255.255.255/32", "fc00::/7", "fe80::/10", "ff00::/8"}
	rules := []obj{
		// DNS от приложений -> Xray (fakedns: мгновенный ответ, домен восстанавливается при соединении)
		{"inboundTag": []string{"tun"}, "port": "53", "outboundTag": tagDNS},
		{"inboundTag": []string{"tun"}, "ip": []string{TunDNS4}, "outboundTag": tagBlock},
		// свои DNS-запросы Xray (для прямых соединений) — напрямую
		{"inboundTag": []string{"dns-internal"}, "outboundTag": tagDirect},
		{"ip": append(private, p.Server+"/32"), "outboundTag": tagDirect},
	}
	if len(p.BlockedDomains) > 0 {
		rules = append(rules, obj{"domain": p.BlockedDomains, "outboundTag": swTCP})
	}
	if len(p.DirectDomains) > 0 {
		rules = append(rules, obj{"domain": p.DirectDomains, "outboundTag": tagDirect})
	}
	for _, s := range p.Services {
		if len(s.Domains) > 0 {
			// QUIC сервиса не пускаем (браузер сразу уходит на TCP — его обходит zapret/ByeDPI)
			rules = append(rules, obj{"domain": s.Domains, "network": "udp", "port": "443", "outboundTag": tagBlock})
			rules = append(rules, obj{"domain": s.Domains, "outboundTag": swService(s.ID)})
		}
	}
	rules = append(rules,
		// QUIC через туннель не пускаем: браузер сразу уходит на TCP — через VLESS это быстрее и стабильнее
		obj{"network": "udp", "port": "443", "outboundTag": tagBlock},
		// российские IP по UDP (игровые серверы и т.п.) — напрямую. Для TCP нельзя: у YouTube/Apple/TikTok есть
		// CDN-узлы в РФ с российскими IP — они ушли бы напрямую и попали под замедление
		obj{"network": "udp", "ip": []string{"geoip:ru"}, "outboundTag": tagDirect},
		obj{"network": "udp", "outboundTag": swUDP},
		obj{"network": "tcp", "outboundTag": swTCP},
	)

	// DNS: российские домены и служебные (проверка интернета Windows, локальные имена) — настоящие IP от Яндекса;
	// остальное — fakedns. Для своих прямых соединений Xray резолвит через DoH (без подмены провайдером).
	ruDNS := []string{"domain:msftncsi.com", "domain:msftconnecttest.com", "domain:lan", "domain:local",
		"domain:localdomain", "domain:home.arpa", "regexp:^[^.]+$", "domain:ntp.org", "domain:time.windows.com",
		"domain:stun.l.google.com"}
	for _, d := range p.DirectDomains {
		if strings.HasPrefix(d, "domain:") || strings.HasPrefix(d, "geosite:") || strings.HasPrefix(d, "full:") {
			ruDNS = append(ruDNS, d)
		}
	}
	dns := obj{
		"tag":           "dns-internal",
		"queryStrategy": "UseIPv4",
		"servers": []any{
			// заблокированные сайты в зоне .ru — fakedns (домен уйдёт на сервер, а не IP-заглушка)
			obj{"address": "fakedns", "domains": append([]string{"full:fakedns-placeholder.invalid"}, p.BlockedDomains...), "skipFallback": true},
			obj{"address": "77.88.8.8", "domains": ruDNS, "skipFallback": true},
			"fakedns",
			obj{"address": "77.88.8.8", "skipFallback": false},
			obj{"address": "77.88.8.1", "skipFallback": false},
		},
	}

	logCfg := obj{"loglevel": e.opt.LogLevel, "access": "none", "dnsLog": false}
	if e.opt.ErrorLog != "" {
		logCfg["error"] = e.opt.ErrorLog
	}
	if e.opt.AccessLog != "" {
		logCfg["access"] = e.opt.AccessLog
	}
	return obj{
		"log": logCfg,
		"inbounds": []obj{{
			"tag": "tun", "protocol": "tun", "port": 0,
			"settings": obj{"name": e.opt.TunName, "MTU": TunMTU, "userLevel": 0},
			"sniffing": obj{"enabled": true, "destOverride": []string{"fakedns", "http", "tls", "quic"},
				// домен из SNI — только для маршрутизации; сервер сам ещё раз определит домен и выберет ближний CDN
				"routeOnly": true, "domainsExcluded": []string{"courier.push.apple.com"}},
		}},
		"outbounds": outbounds,
		"routing":   obj{"domainStrategy": "AsIs", "rules": rules},
		"dns":       dns,
		"fakedns":   []obj{{"ipPool": "198.18.0.0/15", "poolSize": 65535}},
		"policy": obj{"levels": obj{"0": obj{"handshake": 5, "connIdle": 300, "uplinkOnly": 5, "downlinkOnly": 10, "bufferSize": 8192}},
			"system": obj{}},
	}
}
