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

// первый круг: текущий выбран «кто первый ответил» — переходим на приоритетный, если он тоже ответил
func TestChooseStartupPriority(t *testing.T) {
	order := []string{tagHy2, tagVLESS}
	st := map[string]*streak{tagHy2: {}, tagVLESS: {}, tagXHTTP: {}}
	upd(st[tagHy2], true)
	upd(st[tagVLESS], true)
	upd(st[tagXHTTP], true)
	if got := choose(order, st, tagXHTTP); got != tagHy2 {
		t.Fatalf("cur not in order: want hy2, got %s", got)
	}
	if got := choose(order, st, tagVLESS); got != tagHy2 {
		t.Fatalf("short history: want hy2, got %s", got)
	}
}

// плохая сеть: все протоколы проходят через раз — не метаться, держаться за Hysteria2
func TestChooseLossyStays(t *testing.T) {
	order := []string{tagHy2, tagVLESS, tagXHTTP}
	p := map[string]float64{tagHy2: 0.7, tagVLESS: 0.3, tagXHTTP: 0.3}
	st := map[string]*streak{}
	for _, k := range order {
		st[k] = &streak{}
	}
	r := rand.New(rand.NewSource(7))
	cur, switches := tagHy2, 0
	for i := 0; i < 300; i++ {
		for _, k := range order {
			upd(st[k], r.Float64() < p[k])
		}
		if b := choose(order, st, cur); b != "" && b != cur {
			switches++
			cur = b
		}
	}
	t.Logf("switches=%d", switches)
	if switches > 12 {
		t.Fatalf("too many switches on lossy net: %d", switches)
	}
}

// смена сети: история сброшена — первый же сбой текущего при живом запасном даёт переход;
// при длинной хорошей истории один-два сбоя терпим, четыре подряд — обрыв, уходим
func TestChooseFailoverAfterNetSwitch(t *testing.T) {
	order := []string{tagHy2, tagVLESS, tagXHTTP}
	st := map[string]*streak{tagHy2: {}, tagVLESS: {}, tagXHTTP: {}}
	upd(st[tagHy2], false)
	upd(st[tagVLESS], true)
	upd(st[tagXHTTP], true)
	if got := choose(order, st, tagHy2); got != tagVLESS {
		t.Fatalf("свежая история: уходим на vless, got %s", got)
	}

	st = map[string]*streak{tagHy2: {}, tagVLESS: {}, tagXHTTP: {}}
	for i := 0; i < 10; i++ {
		upd(st[tagHy2], true)
		upd(st[tagVLESS], i%2 == 0)
		upd(st[tagXHTTP], i%2 == 0)
	}
	for i := 1; i <= 4; i++ {
		upd(st[tagHy2], false)
		upd(st[tagVLESS], true)
		upd(st[tagXHTTP], true)
		got := choose(order, st, tagHy2)
		if i <= 1 && got != tagHy2 {
			t.Fatalf("%d сбой при хорошей истории — держим hy2, got %s", i, got)
		}
		if i == 4 && got == tagHy2 {
			t.Fatalf("4 сбоя подряд — должны уйти, got %s", got)
		}
	}
}
