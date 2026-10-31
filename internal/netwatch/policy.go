package netwatch

import (
	"time"
)

// Policy — когда серия сбоев превращается в действие. Защита от дёрганья:
// сбои одной «пачки» (упавшая страница = несколько параллельных соединений)
// считаются одним инцидентом; мало одного инцидента; после действия домен
// «остывает»; есть общий лимит.
type Policy struct {
	FailsNeeded int           // сколько инцидентов по домену нужно (по умолчанию 2)
	Window      time.Duration // окно, в котором считаем инциденты
	IncidentGap time.Duration // сбои чаще этого считаются одной пачкой
	Cooldown    time.Duration // пауза по домену после действия
	MinInterval time.Duration // минимальная пауза между любыми действиями
	MaxPerHour  int           // максимум действий в час

	now          func() time.Time
	fails        map[string][]time.Time
	lastFail     map[string]time.Time
	lastAction   map[string]time.Time
	cooldownHint map[string]time.Duration // индивидуальный кулдаун (после неудачи)
	actions      []time.Time
}

// NewPolicy — значения по умолчанию: 2 инцидента за 10 минут (инциденты
// разделены минимум 10 секундами), пауза по домену 6 часов, между
// действиями не чаще раза в минуту, не больше 3 в час.
func NewPolicy() *Policy {
	return &Policy{
		FailsNeeded:  2,
		Window:       10 * time.Minute,
		IncidentGap:  10 * time.Second,
		Cooldown:     6 * time.Hour,
		MinInterval:  time.Minute,
		MaxPerHour:   3,
		now:          time.Now,
		fails:        make(map[string][]time.Time),
		lastFail:     make(map[string]time.Time),
		lastAction:   make(map[string]time.Time),
		cooldownHint: make(map[string]time.Duration),
	}
}

// SetNow подменяет часы (для тестов).
func (p *Policy) SetNow(f func() time.Time) { p.now = f }

// Observe учитывает событие. ok=true — по домену пора запускать автолечение.
// Здоровый поток сбрасывает счётчик сбоев: «сломался, но потом открылся» —
// значит это был транзиент, а не блокировка.
func (p *Policy) Observe(e Event) (domain string, ok bool) {
	if e.Kind == KindHealthy {
		delete(p.fails, e.Domain)
		delete(p.lastFail, e.Domain)
		return "", false
	}
	t := e.At
	if t.IsZero() {
		t = p.now()
	}
	p.applyDefaults()

	// пачка сбоев (одна упавшая страница = несколько соединений) — 1 инцидент
	if last, exists := p.lastFail[e.Domain]; exists && t.Sub(last) < p.IncidentGap {
		return "", false
	}
	p.lastFail[e.Domain] = t

	// инциденты в пределах окна
	list := p.fails[e.Domain]
	keep := list[:0]
	for _, ts := range list {
		if t.Sub(ts) < p.Window {
			keep = append(keep, ts)
		}
	}
	keep = append(keep, t)
	p.fails[e.Domain] = keep
	if len(keep) < p.FailsNeeded {
		return "", false
	}
	if last, exists := p.lastAction[e.Domain]; exists && t.Sub(last) < p.cooldownFor(e.Domain) {
		return "", false
	}
	// глобальный лимит
	acts := p.actions[:0]
	for _, ts := range p.actions {
		if t.Sub(ts) < time.Hour {
			acts = append(acts, ts)
		}
	}
	p.actions = acts
	if len(acts) >= p.MaxPerHour {
		return "", false
	}
	if len(acts) > 0 && t.Sub(acts[len(acts)-1]) < p.MinInterval {
		return "", false
	}
	p.actions = append(p.actions, t)
	p.lastAction[e.Domain] = t
	delete(p.cooldownHint, e.Domain)
	delete(p.fails, e.Domain) // после действия — с чистого листа
	return e.Domain, true
}

// ForceCooldown ставит домену свою паузу (например, короткую после неудачной
// попытки лечения, чтобы не сжигать полный кулдаун).
func (p *Policy) ForceCooldown(domain string, d time.Duration) {
	p.applyDefaults()
	p.lastAction[domain] = p.now()
	p.cooldownHint[domain] = d
}

func (p *Policy) cooldownFor(domain string) time.Duration {
	if d, ok := p.cooldownHint[domain]; ok {
		return d
	}
	return p.Cooldown
}

func (p *Policy) applyDefaults() {
	if p.FailsNeeded <= 0 {
		p.FailsNeeded = 2
	}
	if p.Window <= 0 {
		p.Window = 10 * time.Minute
	}
	if p.IncidentGap <= 0 {
		p.IncidentGap = 10 * time.Second
	}
	if p.Cooldown <= 0 {
		p.Cooldown = 6 * time.Hour
	}
	if p.MinInterval <= 0 {
		p.MinInterval = time.Minute
	}
	if p.MaxPerHour <= 0 {
		p.MaxPerHour = 3
	}
}
