//go:build windows

package main

import (
	"crypto/md5"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"golang.zx2c4.com/wintun"

	"vpnapp/core"
)

const tunName = "VPN"

// physIface — реальный адаптер (Wi-Fi/кабель), через который идёт интернет.
type physIface struct {
	LUID    winipcfg.LUID
	Index   uint32
	Name    string // alias, напр. «Беспроводная сеть»
	Gateway netip.Addr
	WiFi    bool
	MAC     string
}

func tunLUID() (winipcfg.LUID, error) {
	// 1. Ищем адаптер по имени среди всех интерфейсов (надёжно при любых GUID)
	if rows, err := winipcfg.GetIfTable2Ex(winipcfg.MibIfEntryNormal); err == nil {
		for _, row := range rows {
			if strings.EqualFold(row.Alias(), tunName) {
				return row.InterfaceLUID, nil
			}
		}
	}
	// 2. Через Wintun API напрямую
	if ad, err := wintun.OpenAdapter(tunName); err == nil {
		defer ad.Close()
		if l := ad.LUID(); l != 0 {
			return winipcfg.LUID(l), nil
		}
	}
	// 3. Fallback: по детерминированному GUID (md5 от имени)
	id := md5.Sum([]byte(tunName))
	guid := (*windows.GUID)(unsafe.Pointer(&id[0]))
	return winipcfg.LUIDFromGUID(guid)
}

// defaultIface ищет адаптер с маршрутом по умолчанию и наименьшей метрикой (кроме туннелей).
func defaultIface() (*physIface, error) {
	routes, err := winipcfg.GetIPForwardTable2(windows.AF_INET)
	if err != nil {
		return nil, err
	}
	tl, _ := tunLUID()
	type cand struct {
		row    winipcfg.MibIPforwardRow2
		metric uint32
		ifr    *winipcfg.MibIfRow2
	}
	var cs []cand
	for _, r := range routes {
		if r.DestinationPrefix.Prefix().Bits() != 0 || r.InterfaceLUID == tl {
			continue
		}
		ifr, err := r.InterfaceLUID.Interface()
		if err != nil || ifr.OperStatus != winipcfg.IfOperStatusUp {
			continue
		}
		switch ifr.Type {
		case winipcfg.IfTypePropVirtual, winipcfg.IfTypeTunnel, winipcfg.IfTypeSoftwareLoopback, winipcfg.IfTypePPP:
			// другие VPN/туннели (Clash, Amnezia, WireGuard) — не «реальный» интернет
			if ifr.Type != winipcfg.IfTypePPP { // PPPoE-подключение — настоящий интернет
				continue
			}
		}
		m := r.Metric
		if ipi, err := r.InterfaceLUID.IPInterface(windows.AF_INET); err == nil {
			m += ipi.Metric
		}
		cs = append(cs, cand{r, m, ifr})
	}
	if len(cs) == 0 {
		return nil, fmt.Errorf("нет подключения к интернету")
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].metric < cs[j].metric })
	c := cs[0]
	p := &physIface{LUID: c.row.InterfaceLUID, Index: c.row.InterfaceIndex, Name: c.ifr.Alias(),
		Gateway: c.row.NextHop.Addr(), WiFi: c.ifr.Type == winipcfg.IfTypeIEEE80211,
		MAC: fmt.Sprintf("%x", c.ifr.PhysicalAddress())}
	return p, nil
}

// netKey — «отпечаток» сети для запоминания удачного обхода: адаптер + шлюз.
func netKey(p *physIface) string {
	if p == nil {
		return "default"
	}
	return p.MAC + "/" + p.Gateway.String()
}

// configureTun — адрес, маршруты и DNS туннеля. Весь IPv4/IPv6 идёт в туннель двумя «половинками»
// (0/1 и 128/1 — точнее маршрута по умолчанию, но его не трогаем), локальная сеть остаётся на адаптере.
func configureTun(phys *physIface, server string) error {
	var luid winipcfg.LUID
	var err error
	for i := 0; i < 50; i++ { // адаптер появляется не мгновенно
		if luid, err = tunLUID(); err == nil {
			if _, err = luid.Interface(); err == nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("адаптер туннеля: %w", err)
	}
	for _, fam := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		ipi, err := luid.IPInterface(fam)
		if err != nil {
			continue
		}
		ipi.UseAutomaticMetric = false
		ipi.Metric = 1
		ipi.NLMTU = core.TunMTU
		ipi.DadTransmits = 0
		ipi.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		_ = ipi.Set()
	}
	if err := luid.SetIPAddressesForFamily(windows.AF_INET, []netip.Prefix{netip.MustParsePrefix(core.TunAddr4 + "/30")}); err != nil {
		return fmt.Errorf("адрес туннеля: %w", err)
	}
	addRoute := func(fam winipcfg.AddressFamily, prefix netip.Prefix) error {
		row := winipcfg.MibIPforwardRow2{}
		row.Init()
		row.InterfaceLUID = luid
		row.Metric = 0
		if err := row.DestinationPrefix.SetPrefix(prefix); err != nil {
			return err
		}
		if prefix.Addr().Is4() {
			row.NextHop.SetAddr(netip.IPv4Unspecified())
		} else {
			row.NextHop.SetAddr(netip.IPv6Unspecified())
		}
		return row.Create()
	}

	// IPv4-маршруты обязательны (весь трафик в туннель двумя половинами: 0/1 и 128/1)
	for _, p := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if err := addRoute(windows.AF_INET, netip.MustParsePrefix(p)); err != nil {
			// если маршрут уже есть, пробуем обновить/пропустить
			logf("маршрут %s: %v", p, err)
		}
	}

	// IPv6 — по возможности (на ПК может быть отключён; не должно ломать подключение)
	_ = luid.SetIPAddressesForFamily(windows.AF_INET6, []netip.Prefix{netip.MustParsePrefix(core.TunAddr6 + "/126")})
	for _, p := range []string{"::/1", "8000::/1"} {
		_ = addRoute(windows.AF_INET6, netip.MustParsePrefix(p))
	}

	if err := luid.SetDNS(windows.AF_INET, []netip.Addr{netip.MustParseAddr(core.TunDNS4)}, nil); err != nil {
		return fmt.Errorf("DNS туннеля: %w", err)
	}
	// сервер — всегда мимо туннеля (исходящие Xray и так привязаны к адаптеру; это страховка для UDP)
	if phys != nil && phys.Gateway.IsValid() && !phys.Gateway.IsUnspecified() {
		if a, err := netip.ParseAddr(server); err == nil {
			_ = phys.LUID.DeleteRoute(netip.PrefixFrom(a, 32), phys.Gateway)
			_ = phys.LUID.AddRoute(netip.PrefixFrom(a, 32), phys.Gateway, 0)
		}
	}
	flushDNS()
	return nil
}

func removeServerRoute(phys *physIface, server string) {
	if phys == nil || !phys.Gateway.IsValid() {
		return
	}
	if a, err := netip.ParseAddr(server); err == nil {
		_ = phys.LUID.DeleteRoute(netip.PrefixFrom(a, 32), phys.Gateway)
	}
}

// cleanupTun полностью удаляет маршруты туннеля, сбрасывает DNS туннеля и освобождает трафик при выключении.
func cleanupTun(phys *physIface, server string) {
	if tl, err := tunLUID(); err == nil {
		for _, p := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
			_ = tl.DeleteRoute(netip.MustParsePrefix(p), netip.IPv4Unspecified())
		}
		for _, p := range []string{"::/1", "8000::/1"} {
			_ = tl.DeleteRoute(netip.MustParsePrefix(p), netip.IPv6Unspecified())
		}
		_ = tl.FlushRoutes(windows.AF_INET)
		_ = tl.FlushRoutes(windows.AF_INET6)
		_ = tl.FlushDNS(windows.AF_INET)
		_ = tl.FlushDNS(windows.AF_INET6)
		_ = tl.FlushIPAddresses(windows.AF_INET)
	}
	if ad, err := wintun.OpenAdapter(tunName); err == nil {
		_ = ad.Close()
	}
	if phys != nil && server != "" {
		removeServerRoute(phys, server)
	}
	flushDNS()
}

var (
	dnsapi                = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsFlushResolverCache = dnsapi.NewProc("DnsFlushResolverCache")
)

func flushDNS() { _, _, _ = procDnsFlushResolverCache.Call() }

// Windows рассылает DNS-запросы во все адаптеры сразу; пока VPN включён, отвечать должен туннель.
// Политику ставим на время подключения и возвращаем как было.
const dnsPolicyKey = `SOFTWARE\Policies\Microsoft\Windows NT\DNSClient`

func setSmartDNS(disable bool, st *persist) {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, dnsPolicyKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if disable {
		if v, _, err := k.GetIntegerValue("DisableSmartNameResolution"); err == nil {
			st.PrevSmartDNS = int(v)
		} else {
			st.PrevSmartDNS = -1
		}
		_ = k.SetDWordValue("DisableSmartNameResolution", 1)
		return
	}
	if st.PrevSmartDNS == -1 {
		_ = k.DeleteValue("DisableSmartNameResolution")
	} else if st.PrevSmartDNS >= 0 {
		_ = k.SetDWordValue("DisableSmartNameResolution", uint32(st.PrevSmartDNS))
	}
	st.PrevSmartDNS = -2
}

// netWatcher сообщает о смене реального адаптера (Wi-Fi <-> кабель, другой роутер, выход из сна).
type netWatcher struct {
	mu     sync.Mutex
	cb     *winipcfg.RouteChangeCallback
	timer  *time.Timer
	onChange func()
}

func watchNetwork(onChange func()) *netWatcher {
	w := &netWatcher{onChange: onChange}
	w.cb, _ = winipcfg.RegisterRouteChangeCallback(func(t winipcfg.MibNotificationType, r *winipcfg.MibIPforwardRow2) {
		if r == nil || r.DestinationPrefix.Prefix().Bits() != 0 {
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.timer != nil {
			w.timer.Stop()
		}
		w.timer = time.AfterFunc(1500*time.Millisecond, w.onChange)
	})
	return w
}

func (w *netWatcher) Stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cb != nil {
		_ = w.cb.Unregister()
	}
	if w.timer != nil {
		w.timer.Stop()
	}
}

// bindDialer: сокеты самого приложения (подписка, пинг) — через реальный адаптер, мимо туннеля.
func bindDialer(index uint32) {
	core.DirectDialer.Control = func(network, address string, c syscall.RawConn) error {
		var e error
		_ = c.Control(func(fd uintptr) {
			if len(address) > 0 && address[0] == '[' { // IPv6
				e = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, 31 /*IPV6_UNICAST_IF*/, int(index))
				return
			}
			var b [4]byte // IP_UNICAST_IF ждёт индекс в сетевом порядке байт
			b[0], b[1], b[2], b[3] = byte(index>>24), byte(index>>16), byte(index>>8), byte(index)
			e = windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, 31 /*IP_UNICAST_IF*/, int(*(*uint32)(unsafe.Pointer(&b[0]))))
		})
		return e
	}
}
