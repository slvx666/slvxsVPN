// geotrim: оставляет в geoip.dat/geosite.dat только нужные категории (для приложения).
// geotrim list <file>            — список категорий
// geotrim trim <in> <out> code1,code2 — вырезать
package main

import (
	"fmt"
	"os"
	"strings"

	geodata "github.com/xtls/xray-core/app/router"
	"google.golang.org/protobuf/proto"
)

func main() {
	in := os.Args[2]
	b, err := os.ReadFile(in)
	if err != nil {
		panic(err)
	}
	var site geodata.GeoSiteList
	var ip geodata.GeoIPList
	isSite := strings.Contains(in, "site")
	if isSite {
		must(proto.Unmarshal(b, &site))
	} else {
		must(proto.Unmarshal(b, &ip))
	}
	if os.Args[1] == "list" {
		for _, e := range site.Entry {
			fmt.Println(e.CountryCode, len(e.Domain))
		}
		for _, e := range ip.Entry {
			fmt.Println(e.CountryCode, len(e.Cidr))
		}
		return
	}
	want := map[string]bool{}
	for _, c := range strings.Split(os.Args[4], ",") {
		want[strings.ToUpper(c)] = true
	}
	var out []byte
	n := 0
	if isSite {
		var r geodata.GeoSiteList
		for _, e := range site.Entry {
			code := strings.ToUpper(e.CountryCode)
			if want[code] {
				r.Entry = append(r.Entry, e)
				n++
			}
			// <код>@RU — только российские домены категории (.ru/.su/.рф), напр. RU-BLOCKED@RU:
			// заблокированные сайты в зоне .ru, которые иначе ушли бы напрямую
			if want[code+"@RU"] {
				f := &geodata.GeoSite{CountryCode: code + "-RU"}
				for _, d := range e.Domain {
					v := strings.ToLower(d.Value)
					if d.Type != geodata.Domain_Regex && (strings.HasSuffix(v, ".ru") || strings.HasSuffix(v, ".su") ||
						strings.HasSuffix(v, ".xn--p1ai") || v == "ru" || v == "su") {
						f.Domain = append(f.Domain, d)
					}
				}
				fmt.Fprintf(os.Stderr, "%s: %d domains\n", f.CountryCode, len(f.Domain))
				r.Entry = append(r.Entry, f)
				n++
			}
		}
		out, err = proto.Marshal(&r)
	} else {
		var r geodata.GeoIPList
		for _, e := range ip.Entry {
			if want[strings.ToUpper(e.CountryCode)] {
				r.Entry = append(r.Entry, e)
				n++
			}
		}
		out, err = proto.Marshal(&r)
	}
	must(err)
	if n != len(want) {
		fmt.Fprintf(os.Stderr, "found %d of %d categories\n", n, len(want))
		os.Exit(1)
	}
	must(os.WriteFile(os.Args[3], out, 0o644))
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
