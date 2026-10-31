package netwatch

import (
	"sync"
	"testing"
	"time"
)

func testDetector(now *time.Time) (*Detector, *[]Event) {
	d := NewDetector(5 * time.Second)
	d.SetNow(func() time.Time { return *now })
	var events []Event
	d.OnEvent = func(e Event) { events = append(events, e) }
	return d, &events
}

func chFor(t *testing.T, sni string) Packet {
	t.Helper()
	raw := ipv4TCP(t, "10.0.0.5", "203.0.113.10", 51234, 443, FlagPSH|FlagACK, buildClientHello(sni))
	p, ok := ParsePacket(raw)
	if !ok {
		t.Fatal("ClientHello-пакет не разобрался")
	}
	return p
}

func inbound(flags uint8, payload []byte) Packet {
	return Packet{
		SrcIP:   parseIP("203.0.113.10"),
		DstIP:   parseIP("10.0.0.5"),
		SrcPort: 443,
		DstPort: 51234,
		Flags:   flags,
		Payload: payload,
	}
}

func TestDetectorHealthy(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	d.OnPacket(chFor(t, "example.com"), true)
	if d.ActiveFlows() != 1 {
		t.Fatalf("поток не зарегистрирован")
	}
	d.OnPacket(inbound(FlagACK, []byte("server hello")), false)
	if d.ActiveFlows() != 0 {
		t.Fatalf("поток должен закрыться после ответа")
	}
	if len(*events) != 1 || (*events)[0].Kind != KindHealthy || (*events)[0].Domain != "example.com" {
		t.Fatalf("события: %+v", *events)
	}
	// таймаут уже не должен сработать повторно
	now = now.Add(10 * time.Second)
	d.Tick()
	if len(*events) != 1 {
		t.Fatalf("после ответа таймаут невозможен: %+v", *events)
	}
}

func TestDetectorReset(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	d.OnPacket(chFor(t, "zendesk.com"), true)
	d.OnPacket(inbound(FlagRST, nil), false)
	if len(*events) != 1 || (*events)[0].Kind != KindReset {
		t.Fatalf("ожидался сброс: %+v", *events)
	}
	if d.ActiveFlows() != 0 {
		t.Fatal("поток должен закрыться после RST")
	}
}

func TestDetectorTimeout(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	d.OnPacket(chFor(t, "blocked.example"), true)
	now = now.Add(4 * time.Second)
	d.Tick()
	if len(*events) != 0 {
		t.Fatalf("рано: %+v", *events)
	}
	now = now.Add(2 * time.Second)
	d.Tick()
	if len(*events) != 1 || (*events)[0].Kind != KindTimeout || (*events)[0].Domain != "blocked.example" {
		t.Fatalf("ожидался таймаут: %+v", *events)
	}
	if d.ActiveFlows() != 0 {
		t.Fatal("после таймаута поток закрывается")
	}
	d.Tick() // повторный тик не должен дублировать событие
	if len(*events) != 1 {
		t.Fatalf("дубликат таймаута: %+v", *events)
	}
}

func TestDetectorTwoFlows(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	p1 := chFor(t, "a.example")
	p2 := chFor(t, "b.example")
	p2.DstPort = 8443
	p2.SrcPort = 51235
	d.OnPacket(p1, true)
	d.OnPacket(p2, true)
	if d.ActiveFlows() != 2 {
		t.Fatalf("оба потока должны отслеживаться: %d", d.ActiveFlows())
	}
	answered := inbound(FlagACK, []byte("ok"))
	d.OnPacket(answered, false)
	now = now.Add(10 * time.Second)
	d.Tick()
	if len(*events) != 2 {
		t.Fatalf("ожидали 2 события: %+v", *events)
	}
	kinds := map[Kind]int{}
	for _, e := range *events {
		kinds[e.Kind]++
	}
	if kinds[KindHealthy] != 1 || kinds[KindTimeout] != 1 {
		t.Fatalf("ожидали 1 healthy и 1 timeout: %+v", *events)
	}
}

func TestDetectorIgnoresNonClientHello(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	http := Packet{SrcIP: parseIP("10.0.0.5"), DstIP: parseIP("203.0.113.10"), SrcPort: 1, DstPort: 80,
		Flags: FlagPSH, Payload: []byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n")}
	d.OnPacket(http, true)
	if d.ActiveFlows() != 0 || len(*events) != 0 {
		t.Fatalf("HTTP не должен считаться потоком: flows=%d events=%+v", d.ActiveFlows(), *events)
	}
}

func TestDetectorFlowCap(t *testing.T) {
	now := time.Unix(1000, 0)
	d, _ := testDetector(&now)
	d.maxFlows = 2
	for i := 0; i < 5; i++ {
		p := chFor(t, "example.com")
		p.SrcPort = uint16(50000 + i)
		d.OnPacket(p, true)
	}
	if d.ActiveFlows() > 2 {
		t.Fatalf("потоки не ограничены: %d", d.ActiveFlows())
	}
}

func TestDetectorSegmentedClientHello(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	ch := buildClientHello("segmented.example")
	// режем ClientHello на два TCP-сегмента: современный Chrome с PQ key share
	// присылает его не одним пакетом
	p1, _ := ParsePacket(ipv4TCPSeq(t, "10.0.0.5", "203.0.113.10", 51234, 443, 1000, FlagPSH|FlagACK, ch[:10]))
	p2, _ := ParsePacket(ipv4TCPSeq(t, "10.0.0.5", "203.0.113.10", 51234, 443, 1010, FlagPSH|FlagACK, ch[10:]))
	d.OnPacket(p1, true)
	d.OnPacket(p2, true)
	if d.ActiveFlows() != 1 {
		t.Fatalf("поток должен отслеживаться после сборки")
	}
	d.OnPacket(inbound(FlagACK, []byte("server hello")), false)
	if len(*events) != 1 || (*events)[0].Domain != "segmented.example" || (*events)[0].Kind != KindHealthy {
		t.Fatalf("SNI из собранного ClientHello: %+v", *events)
	}
}

func TestDetectorRetransmitFirstSegment(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	ch := buildClientHello("retrans.example")
	p1, _ := ParsePacket(ipv4TCPSeq(t, "10.0.0.5", "203.0.113.10", 51234, 443, 5000, FlagPSH|FlagACK, ch[:10]))
	dup, _ := ParsePacket(ipv4TCPSeq(t, "10.0.0.5", "203.0.113.10", 51234, 443, 5000, FlagPSH|FlagACK, ch[:10]))
	d.OnPacket(p1, true)
	d.OnPacket(dup, true) // ретрансмит не должен ломать сборку
	rest, _ := ParsePacket(ipv4TCPSeq(t, "10.0.0.5", "203.0.113.10", 51234, 443, 5010, FlagPSH|FlagACK, ch[10:]))
	d.OnPacket(rest, true)
	d.OnPacket(inbound(FlagACK, []byte("ok")), false)
	if len(*events) != 1 || (*events)[0].Domain != "retrans.example" {
		t.Fatalf("SNI после ретрансмита: %+v", *events)
	}
}

func TestDetectorPendingHelloExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	d, events := testDetector(&now)
	ch := buildClientHello("pending.example")
	p1, _ := ParsePacket(ipv4TCPSeq(t, "10.0.0.5", "203.0.113.10", 51234, 443, 1000, FlagPSH|FlagACK, ch[:10]))
	d.OnPacket(p1, true)
	if d.ActiveFlows() != 1 {
		t.Fatalf("поток с неполным ClientHello должен ждать второй сегмент")
	}
	now = now.Add(11 * time.Second)
	d.Tick()
	if d.ActiveFlows() != 0 {
		t.Fatalf("незавершённая сборка ClientHello должна удаляться по таймауту")
	}
	if len(*events) != 0 {
		t.Fatalf("по неполному ClientHello событий быть не должно: %+v", *events)
	}
}

// TestDetectorConcurrent — раньше Tick и OnPacket работали с картой без
// мьютекса и валили процесс «concurrent map iteration and map write».
func TestDetectorConcurrent(t *testing.T) {
	now := time.Unix(1000, 0)
	d, _ := testDetector(&now)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(portBase int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				p := chFor(t, "concurrent.example")
				p.SrcPort = uint16(portBase + i)
				d.OnPacket(p, true)
				d.OnPacket(inbound(FlagACK, []byte("ok")), false)
			}
		}(40000 + g*1000)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			d.Tick()
		}
	}()
	wg.Wait()
}

func parseIP(s string) []byte {
	var b []byte
	for _, part := range splitDots(s) {
		b = append(b, byte(atoiSmall(part)))
	}
	return b
}

func splitDots(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '.' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

func atoiSmall(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
