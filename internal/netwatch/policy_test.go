package netwatch

import (
	"testing"
	"time"
)

func failAt(domain string, at time.Time) Event {
	return Event{Domain: domain, Kind: KindTimeout, At: at}
}

func healthyAt(domain string, at time.Time) Event {
	return Event{Domain: domain, Kind: KindHealthy, At: at}
}

func TestPolicyNeedsTwoFails(t *testing.T) {
	p := NewPolicy()
	base := time.Unix(1000, 0)
	if d, ok := p.Observe(failAt("a.example", base)); ok {
		t.Fatalf("один сбой не повод: %s", d)
	}
	if d, ok := p.Observe(failAt("a.example", base.Add(30*time.Second))); !ok || d != "a.example" {
		t.Fatalf("два сбоя — пора лечить")
	}
}

func TestPolicyHealthyResets(t *testing.T) {
	p := NewPolicy()
	base := time.Unix(1000, 0)
	p.Observe(failAt("a.example", base))
	p.Observe(healthyAt("a.example", base.Add(time.Minute)))
	// счётчик сброшен — одиночного сбоя снова мало
	if _, ok := p.Observe(failAt("a.example", base.Add(2*time.Minute))); ok {
		t.Fatal("после успешного открытия счётчик сбоев должен сброситься")
	}
	if _, ok := p.Observe(failAt("a.example", base.Add(3*time.Minute))); !ok {
		t.Fatal("два сбоя после сброса — пора лечить")
	}
}

func TestPolicyWindowExpiry(t *testing.T) {
	p := NewPolicy()
	base := time.Unix(1000, 0)
	p.Observe(failAt("a.example", base))
	// второй сбой далеко за окном — считается первым
	if _, ok := p.Observe(failAt("a.example", base.Add(20*time.Minute))); ok {
		t.Fatal("сбои за пределами окна не суммируются")
	}
}

func TestPolicyCooldown(t *testing.T) {
	p := NewPolicy()
	base := time.Unix(1000, 0)
	p.Observe(failAt("a.example", base))
	if _, ok := p.Observe(failAt("a.example", base.Add(time.Minute))); !ok {
		t.Fatal("первое лечение должно сработать")
	}
	// сразу после действия — кулдаун по домену
	p.Observe(failAt("a.example", base.Add(2*time.Minute)))
	if _, ok := p.Observe(failAt("a.example", base.Add(3*time.Minute))); ok {
		t.Fatal("в кулдауне лечение запрещено")
	}
	// после кулдауна (6 ч) — снова можно
	late := base.Add(7 * time.Hour)
	p.Observe(failAt("a.example", late))
	if _, ok := p.Observe(failAt("a.example", late.Add(time.Minute))); !ok {
		t.Fatal("после кулдауна лечение снова разрешено")
	}
}

func TestPolicyGlobalLimits(t *testing.T) {
	p := NewPolicy()
	p.IncidentGap = time.Nanosecond
	p.MaxPerHour = 2
	p.MinInterval = time.Nanosecond
	base := time.Unix(1000, 0)
	domains := []string{"a.example", "b.example", "c.example"}
	for i, d := range domains {
		at := base.Add(time.Duration(i) * 10 * time.Minute)
		p.Observe(failAt(d, at))
		_, ok := p.Observe(failAt(d, at.Add(time.Second)))
		if i < 2 && !ok {
			t.Fatalf("%s: должно было сработать", d)
		}
		if i == 2 && ok {
			t.Fatalf("%s: лимит 2 в час должен заблокировать", d)
		}
	}
}

func TestPolicyMinInterval(t *testing.T) {
	p := NewPolicy()
	p.IncidentGap = time.Nanosecond
	p.MinInterval = 5 * time.Minute
	base := time.Unix(1000, 0)
	p.Observe(failAt("a.example", base))
	if _, ok := p.Observe(failAt("a.example", base.Add(time.Second))); !ok {
		t.Fatal("первое действие разрешено")
	}
	p.Observe(failAt("b.example", base.Add(time.Minute)))
	if _, ok := p.Observe(failAt("b.example", base.Add(2*time.Minute))); ok {
		t.Fatal("между действиями должна быть пауза")
	}
}

func TestPolicyIncidentGap(t *testing.T) {
	// одна упавшая страница = пачка параллельных соединений: сбои в пределах
	// IncidentGap считаются одним инцидентом и не запускают лечение
	p := NewPolicy()
	p.MinInterval = time.Nanosecond
	base := time.Unix(1000, 0)
	p.Observe(failAt("slow.example", base))
	p.Observe(failAt("slow.example", base.Add(2*time.Second)))
	p.Observe(failAt("slow.example", base.Add(4*time.Second)))
	if _, ok := p.Observe(failAt("slow.example", base.Add(6*time.Second))); ok {
		t.Fatal("пачка сбоев одной страницы не должна запускать лечение")
	}
	if _, ok := p.Observe(failAt("slow.example", base.Add(15*time.Second))); !ok {
		t.Fatal("второй инцидент должен запускать лечение")
	}
}

func TestPolicyForceCooldown(t *testing.T) {
	p := NewPolicy()
	p.IncidentGap = time.Nanosecond
	p.MinInterval = time.Nanosecond
	base := time.Unix(1000, 0)
	p.Observe(failAt("a.example", base))
	if _, ok := p.Observe(failAt("a.example", base.Add(time.Second))); !ok {
		t.Fatal("первое лечение должно сработать")
	}
	// лечение не помогло — ставим короткий кулдаун вместо 6 часов
	p.SetNow(func() time.Time { return base.Add(time.Second) })
	p.ForceCooldown("a.example", 30*time.Minute)
	p.Observe(failAt("a.example", base.Add(2*time.Minute)))
	if _, ok := p.Observe(failAt("a.example", base.Add(3*time.Minute))); ok {
		t.Fatal("в коротком кулдауне лечение запрещено")
	}
	p.Observe(failAt("a.example", base.Add(40*time.Minute)))
	if _, ok := p.Observe(failAt("a.example", base.Add(41*time.Minute))); !ok {
		t.Fatal("после короткого кулдауна снова можно")
	}
}

func TestPolicyDifferentDomains(t *testing.T) {
	p := NewPolicy()
	p.IncidentGap = time.Nanosecond
	p.MinInterval = time.Nanosecond
	base := time.Unix(1000, 0)
	p.Observe(failAt("a.example", base))
	p.Observe(failAt("a.example", base.Add(time.Second)))
	// b.example — отдельная история, его сбои не смешиваются с a.example
	if _, ok := p.Observe(failAt("b.example", base.Add(2*time.Second))); ok {
		t.Fatal("у другого домена ещё один сбой — рано")
	}
	if _, ok := p.Observe(failAt("b.example", base.Add(3*time.Second))); !ok {
		t.Fatal("у b.example два сбоя — пора")
	}
}
