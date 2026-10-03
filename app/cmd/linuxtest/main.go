// linuxtest — проверка движка на сервере (Linux, в сетевом пространстве имён): те же конфиг, проверки и обход,
// что в приложениях, только TUN создаётся Xray по имени, а исходящие привязаны к -iface.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"vpnapp/bdpi"
	"vpnapp/core"
)

func main() {
	sub := flag.String("sub", "", "ссылка подписки")
	iface := flag.String("iface", "vt0", "реальный интерфейс")
	dir := flag.String("dir", "/tmp/vpnapp-test", "папка данных")
	byedpi := flag.String("byedpi", "", "путь к ByeDPI (пусто — без обхода)")
	flag.Parse()
	os.MkdirAll(*dir, 0o755)
	logf := func(f string, a ...any) { log.Printf(f, a...) }

	core.DirectDialer.Control = func(network, address string, c syscall.RawConn) error {
		var e error
		c.Control(func(fd uintptr) { e = syscall.BindToDevice(int(fd), *iface) })
		return e
	}
	ctx := context.Background()
	p, err := core.FetchProfile(ctx, *sub, "linux")
	if err != nil {
		log.Fatal(core.Detail(err))
	}
	for _, n := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := core.EnsureFile(ctx, p.Files[n], filepath.Join(*dir, n)); err != nil {
			log.Fatal(n, err)
		}
	}
	var bp core.Bypass
	if *byedpi != "" {
		bp = bdpi.New(*byedpi, 10801, logf)
	}
	e := core.NewEngine(core.Options{
		DataDir: *dir, AssetDir: *dir, TunName: "xtun", BindIface: *iface, Platform: "linux", LogLevel: "warning",
		Bypass: bp, Logf: logf,
		OnState: func(s core.State) { b, _ := json.Marshal(s); log.Printf("STATE %s", b) },
		AfterStart: func() error {
			for _, c := range [][]string{
				{"ip", "addr", "replace", core.TunAddr4 + "/30", "dev", "xtun"},
				{"ip", "link", "set", "xtun", "up"},
				{"ip", "route", "replace", "default", "dev", "xtun", "metric", "1"},
			} {
				if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
					return fmt.Errorf("%v: %s", c, out)
				}
			}
			return nil
		},
	})
	if err := e.Start(p); err != nil {
		log.Fatal(err)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for s := range sig {
		if s == syscall.SIGHUP {
			log.Print("restart")
			e.Restart(*iface)
			continue
		}
		break
	}
	e.Stop()
	time.Sleep(200 * time.Millisecond)
}
