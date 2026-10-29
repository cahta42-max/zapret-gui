package netwatch

import (
	"sync"
	"time"
)

// Kind — тип события по потоку.
type Kind int

const (
	// KindHealthy — сервер ответил данными: поток жив, счётчики сбоев сбрасываем.
	KindHealthy Kind = iota
	// KindReset — сервер (или DPI) прислал RST сразу после ClientHello.
	KindReset
	// KindTimeout — ответа не было за timeout.
	KindTimeout
)

// String — для журнала: короткое и понятное человеку описание.
func (k Kind) String() string {
	switch k {
	case KindHealthy:
		return "ответ получен"
	case KindReset:
		return "сброс соединения"
	case KindTimeout:
		return "нет ответа"
	default:
		return "неизвестно"
	}
}

// Event — «что произошло» по домену. Detail — подробность для журнала.
type Event struct {
	Domain  string
	Kind    Kind
	At      time.Time
	Detail  string
	PID     uint32 // процесс-владелец клиентского сокета (0 — не определён)
	Process string // имя процесса: chrome.exe, telegram.exe и т.п.
}

// flowKey — TCP-поток со стороны клиента: клиент → сервер.
type flowKey struct {
	client string
	server string
}

type flow struct {
	domain   string
	deadline time.Time
	created  time.Time
	pid      uint32
	proc     string
	// сборка ClientHello, разрезанного на несколько сегментов
	buf     []byte
	bufSeq  uint32
	haveSNI bool
}

// maxHelloBytes — предел буфера сборки ClientHello (с PQ key share бывает ~2 КБ).
const maxHelloBytes = 16 * 1024

// pendingHelloTTL — сколько держим поток, в котором ClientHello так и не
// собрался (сегменты потерялись/SNI не нашёлся).
const pendingHelloTTL = 10 * time.Second

// Detector — сопоставляет исходящие ClientHello с ответами сервера.
//
// Логика: увидели ClientHello с SNI → запомнили поток и дедлайн. Если до
// дедлайна пришли данные от сервера — поток здоров; если пришёл RST — сбой;
// если ничего не пришло — сбой по таймауту (Tick).
//
// Detector потокобезопасен: OnPacket зовёт горутина чтения пакетов, Tick —
// отдельная горутина таймаутов; карта потоков под мьютексом.
type Detector struct {
	timeout  time.Duration
	maxFlows int
	now      func() time.Time

	mu    sync.Mutex
	flows map[flowKey]*flow

	// OnEvent вызывается БЕЗ удержания мьютекса — обработчик может быть
	// медленнее и не должен блокировать чтение пакетов.
	OnEvent func(Event)

	// resolver определяет процесс по соединению (может быть nil).
	resolver ProcessResolver
}

// NewDetector создаёт детектор без определения процессов. timeout — сколько
// ждём ответ сервера после ClientHello (рекомендуется 4–6 с).
func NewDetector(timeout time.Duration) *Detector {
	return NewDetectorWithResolver(timeout, nil)
}

// NewDetectorWithResolver — детектор, который подписывает события именем
// процесса-владельца соединения.
func NewDetectorWithResolver(timeout time.Duration, resolver ProcessResolver) *Detector {
	return &Detector{
		timeout:  timeout,
		maxFlows: 4096,
		now:      time.Now,
		flows:    make(map[flowKey]*flow),
		resolver: resolver,
	}
}

// SetNow подменяет часы (для тестов).
func (d *Detector) SetNow(f func() time.Time) { d.now = f }

// ActiveFlows — сколько потоков сейчас отслеживается (для тестов/диагностики).
func (d *Detector) ActiveFlows() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.flows)
}

// OnPacket обрабатывает один пакет. outbound — исходящий от нас.
func (d *Detector) OnPacket(p Packet, outbound bool) {
	var events []Event
	d.mu.Lock()
	if outbound {
		events = d.onOutbound(p)
	} else {
		events = d.onInbound(p)
	}
	d.mu.Unlock()
	for _, e := range events {
		d.emit(e)
	}
}

func (d *Detector) onOutbound(p Packet) []Event {
	if len(p.Payload) == 0 {
		return nil
	}
	key := makeKey(p, true)
	f, exists := d.flows[key]
	if exists && f.haveSNI {
		return nil // SNI уже знаем, дальше поток не интересен
	}
	if !exists {
		if len(d.flows) >= d.maxFlows {
			for k := range d.flows { // вытесняем произвольный, чтобы не росла память
				delete(d.flows, k)
				break
			}
		}
		f = &flow{bufSeq: p.Seq, created: d.now()}
		if d.resolver != nil {
			f.pid, f.proc = d.resolver.Lookup(p.SrcIP.String(), p.SrcPort)
		}
		d.flows[key] = f
	}

	// собираем ClientHello по TCP-seq: сегменты могут прийти по частям
	if len(f.buf) == 0 || p.Seq == f.bufSeq+uint32(len(f.buf)) {
		if len(f.buf)+len(p.Payload) <= maxHelloBytes {
			f.buf = append(f.buf, p.Payload...)
		} else {
			delete(d.flows, key)
			return nil
		}
	} else if p.Seq == f.bufSeq {
		f.buf = append(f.buf[:0], p.Payload...) // ретрансмит первого сегмента
	} else {
		// разрыв/перестановка: начинаем сборку заново с этого сегмента
		f.buf = append(f.buf[:0], p.Payload...)
		f.bufSeq = p.Seq
	}

	host, isCH := SNIHost(f.buf)
	if host != "" {
		f.domain = host
		f.haveSNI = true
		f.buf = nil
		f.deadline = d.now().Add(d.timeout)
		return nil
	}
	if !isCH {
		delete(d.flows, key) // не похоже на ClientHello — не наш поток
		return nil
	}
	// запись ещё не целиком — ждём следующий сегмент, но не бесконечно
	if len(f.buf) >= maxHelloBytes || recordComplete(f.buf) {
		delete(d.flows, key)
	}
	return nil
}

func (d *Detector) onInbound(p Packet) []Event {
	key := makeKey(p, false)
	f, exists := d.flows[key]
	if !exists || !f.haveSNI {
		return nil
	}
	now := d.now()
	if p.Flags&FlagRST != 0 {
		delete(d.flows, key)
		return []Event{{Domain: f.domain, Kind: KindReset, At: now, Detail: "сервер прислал RST",
			PID: f.pid, Process: f.proc}}
	}
	if len(p.Payload) > 0 {
		delete(d.flows, key)
		return []Event{{Domain: f.domain, Kind: KindHealthy, At: now, Detail: "ответ сервера",
			PID: f.pid, Process: f.proc}}
	}
	return nil
}

// Tick рассылает таймауты по потокам, не дождавшимся ответа. Вызывать
// периодически (например, раз в 500 мс).
func (d *Detector) Tick() {
	now := d.now()
	var events []Event
	d.mu.Lock()
	for key, f := range d.flows {
		if !f.haveSNI {
			// незавершённая сборка ClientHello: через pendingHelloTTL удаляем,
			// чтобы буферы не копились (на каждый поток до 16 КБ)
			if now.Sub(f.created) > pendingHelloTTL {
				delete(d.flows, key)
			}
			continue // ещё собираем ClientHello — дедлайна нет
		}
		if now.Before(f.deadline) {
			continue
		}
		events = append(events, Event{
			Domain:  f.domain,
			Kind:    KindTimeout,
			At:      now,
			Detail:  d.timeout.String() + " без ответа",
			PID:     f.pid,
			Process: f.proc,
		})
		delete(d.flows, key)
	}
	d.mu.Unlock()
	for _, e := range events {
		d.emit(e)
	}
}

// recordComplete — пришёл ли TLS-record ClientHello целиком (для решения
// «хватит буферизовать, SNI там всё равно нет»).
func recordComplete(buf []byte) bool {
	if len(buf) < 5 || buf[0] != 0x16 {
		return false
	}
	return len(buf) >= 5+int(uint16(buf[3])<<8|uint16(buf[4]))
}

func (d *Detector) emit(e Event) {
	if d.OnEvent != nil {
		d.OnEvent(e)
	}
}

func makeKey(p Packet, outbound bool) flowKey {
	client := p.SrcIP.String() + ":" + itoa(p.SrcPort)
	server := p.DstIP.String() + ":" + itoa(p.DstPort)
	if outbound {
		return flowKey{client: client, server: server}
	}
	return flowKey{client: server, server: client}
}

func itoa(v uint16) string {
	if v == 0 {
		return "0"
	}
	var b [5]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
