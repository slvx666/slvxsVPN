// probe — стенд на ПК: ядро без TUN (права администратора не нужны), те же выходы и проверки, что в приложении.
// Печатает журнал движка и итоговое состояние. Профиль и гео-базы — из %LOCALAPPDATA%\VPN.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"vpnapp/core"
)

func main() {
	dur := flag.Duration("t", 60*time.Second, "сколько работать")
	dir := flag.String("dir", filepath.Join(os.Getenv("LOCALAPPDATA"), "VPN"), "папка профиля и гео-баз")
	flag.Parse()
	p, err := core.LoadProfile(*dir)
	if err != nil {
		log.Fatal(err)
	}
	tmp, _ := os.MkdirTemp("", "vpnprobe")
	logf := func(f string, a ...any) { log.Printf(f, a...) }
	e := core.NewEngine(core.Options{DataDir: tmp, AssetDir: *dir, Platform: "windows", NoTUN: true, Logf: logf,
		OnState: func(s core.State) { log.Printf("STATE %+v", s) }})
	if err := e.Start(p); err != nil {
		log.Fatal(err)
	}
	time.Sleep(*dur)
	fmt.Printf("final: %+v\n", e.State())
	e.Stop()
}
