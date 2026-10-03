package core

import (
	"math/rand"
	"testing"
)

func TestChooseFlaky(t *testing.T) {
	order := []string{tagVLESS, tagXHTTP, tagHy2}
	p := map[string]float64{tagVLESS: 0.3, tagXHTTP: 0.6, tagHy2: 1.0}
	st := map[string]*streak{}
	for _, k := range order {
		st[k] = &streak{}
	}
	r := rand.New(rand.NewSource(1))
	cur, switches := "", 0
	counts := map[string]int{}
	for i := 0; i < 200; i++ {
		for _, k := range order {
			upd(st[k], r.Float64() < p[k])
		}
		if b := choose(order, st, cur); b != "" && b != cur {
			if cur != "" {
				switches++
			}
			cur = b
		}
		if i >= 10 {
			counts[cur]++
		}
	}
	t.Logf("switches=%d counts=%v", switches, counts)
	if counts[tagHy2] < 180 {
		t.Fatalf("hy2 should dominate: %v", counts)
	}
	// всё стабильно — возвращаемся на VLESS
	for k := range p {
		p[k] = 1
	}
	for i := 0; i < 12; i++ {
		for _, k := range order {
			upd(st[k], true)
		}
		cur = choose(order, st, cur)
	}
	if cur != tagVLESS {
		t.Fatalf("want vless when stable, got %s", cur)
	}
}
