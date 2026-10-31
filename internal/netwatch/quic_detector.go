package netwatch

import (
	"sync"
	"time"
)

// QUICDetector — детектор сбоев для QUIC (UDP/443), аналог TCP-детектора.
//
// Логика: из исходящих QUIC Initial собираем CRYPTO-поток (ClientHello может
// быть разрезан на несколько датаграмм), достаём SNI. Если в течение timeout
// от сервера не пришло ни одной датаграммы (любой UDP-ответ на порт клиента) —
// событие KindTimeout. Ответ сервера → KindHealthy.
type QUICDetector struct {
	timeout  time.Duration
	maxFlows int
	now      func() time.Time

	mu    sync.Mutex
	flows map[flowKey]*quicFlow

	// OnEvent вызывается без удержания мьютекса.
	OnEvent  func(Event)
	resolver ProcessResolver
}

type quicFlow struct {
	domain   string
	deadline time.Time
	created  time.Time
	haveSNI  bool
	pid      uint32
	proc     string
	// сборка CRYPTO-потока: куски по смещениям
	crypto      []byte
	cryptoEnded uint64
}

// NewQUICDetector создаёт детектор QUIC без определения процессов.
func NewQUICDetector(timeout time.Duration) *QUICDetector {
	return NewQUICDetectorWithResolver(timeout, nil)
}

// NewQUICDetectorWithResolver — детектор QUIC, который подписывает события
// именем процесса-владельца соединения.
func NewQUICDetectorWithResolver(timeout time.Duration, resolver ProcessResolver) *QUICDetector {
	return &QUICDetector{
		timeout:  timeout,
		maxFlows: 1024,
		now:      time.Now,
		flows:    make(map[flowKey]*quicFlow),
		resolver: resolver,
	}
}

// SetNow подменяет часы (для тестов).
func (d *QUICDetector) SetNow(f func() time.Time) { d.now = f }

// ActiveFlows — сколько потоков отслеживается (тесты/диагностика).
func (d *QUICDetector) ActiveFlows() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.flows)
}

// OnDatagram обрабатывает UDP-датаграмму. outbound — от нас.
func (d *QUICDetector) OnDatagram(p UDPPacket, outbound bool) {
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

func (d *QUICDetector) onOutbound(p UDPPacket) []Event {
	if len(p.Payload) == 0 {
		return nil
	}
	chunk, chunkOff, ok := ParseQUICInitial(p.Payload)
	if !ok || len(chunk) == 0 {
		return nil
	}
	key := makeUDPKey(p, true)
	f, exists := d.flows[key]
	if exists && f.haveSNI {
		return nil
	}
	if !exists {
		if len(d.flows) >= d.maxFlows {
			for k := range d.flows {
				delete(d.flows, k)
				break
			}
		}
		f = &quicFlow{created: d.now()}
		if d.resolver != nil {
			f.pid, f.proc = d.resolver.Lookup(ProtoUDP, p.SrcIP.String(), p.SrcPort)
		}
		d.flows[key] = f
	}
	if chunkOff == f.cryptoEnded {
		// обычный случай: ClientHello целиком в одной датаграмме
		if len(f.crypto)+len(chunk) <= maxHelloBytes {
			f.crypto = append(f.crypto, chunk...)
			f.cryptoEnded = chunkOff + uint64(len(chunk))
		}
	} else if chunkOff == 0 {
		f.crypto = append(f.crypto[:0], chunk...)
		f.cryptoEnded = uint64(len(chunk))
	}
	host, isCH := ClientHelloSNI(f.crypto)
	if host != "" {
		f.domain = host
		f.haveSNI = true
		f.crypto = nil
		f.deadline = d.now().Add(d.timeout)
		return nil
	}
	if !isCH && f.cryptoEnded > 0 {
		// не ClientHello — не наш поток
		delete(d.flows, key)
	}
	return nil
}

func (d *QUICDetector) onInbound(p UDPPacket) []Event {
	key := makeUDPKey(p, false)
	f, exists := d.flows[key]
	if !exists || !f.haveSNI || len(p.Payload) == 0 {
		return nil
	}
	delete(d.flows, key)
	return []Event{{Domain: f.domain, Kind: KindHealthy, At: d.now(), Detail: "ответ сервера (QUIC)",
		PID: f.pid, Process: f.proc}}
}

// Tick рассылает таймауты по QUIC-потокам, не дождавшимся ответа.
func (d *QUICDetector) Tick() {
	now := d.now()
	var events []Event
	d.mu.Lock()
	for key, f := range d.flows {
		if !f.haveSNI {
			if now.Sub(f.created) > pendingHelloTTL {
				delete(d.flows, key)
			}
			continue
		}
		if now.Before(f.deadline) {
			continue
		}
		events = append(events, Event{
			Domain:  f.domain,
			Kind:    KindTimeout,
			At:      now,
			Detail:  d.timeout.String() + " без ответа (QUIC)",
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

func (d *QUICDetector) emit(e Event) {
	if d.OnEvent != nil {
		d.OnEvent(e)
	}
}

// makeUDPKey — поток QUIC (UDP) со стороны клиента.
func makeUDPKey(p UDPPacket, outbound bool) flowKey {
	client := p.SrcIP.String() + ":" + itoa(p.SrcPort)
	server := p.DstIP.String() + ":" + itoa(p.DstPort)
	if !outbound {
		client, server = server, client
	}
	return flowKey{client: client, server: server}
}
