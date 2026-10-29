package netwatch

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"testing"
	"time"
)

// buildQUICInitial собирает настоящий QUIC v1 Initial с ClientHello (SNI host)
// и защитой заголовка — вектор для проверки дешифровки.
func buildQUICInitial(host string) []byte {
	dcid := []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88}
	rec := buildClientHello(host)
	hs := rec[5:] // handshake ClientHello без TLS-записи

	// plaintext: CRYPTO-фрейм с ClientHello + немного PADDING
	frame := []byte{0x06, 0x00}          // type=CRYPTO, offset=0
	frame = append(frame, byte(len(hs))) // length (однобайтовый varint до 63 не влезет — используем 2-байтовый)
	if len(hs) > 63 {
		frame = []byte{0x06}
		frame = appendVarint2(frame, 0)
		frame = appendVarint2(frame, uint64(len(hs)))
	}
	plain := append([]byte{}, frame...)
	plain = append(plain, hs...)

	const pnLen = 2
	pn := uint64(0x0001)

	header := []byte{0x80 | (pnLen - 1)} // long header (0x80), type=Initial v1 (0x00), pnLen
	header = append(header, 0x00, 0x00, 0x00, 0x01)
	header = append(header, byte(len(dcid)))
	header = append(header, dcid...)
	header = append(header, 0x00) // scid len
	header = append(header, 0x00) // token len
	header = appendVarint2(header, uint64(pnLen+len(plain)+quicTagLen))
	pnOffset := len(header)
	header = append(header, byte(pn>>8), byte(pn))

	key, iv, hp := deriveInitialKeys(quicSaltV1, dcid)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	nonce := make([]byte, 12)
	copy(nonce, iv)
	nonce[10] ^= byte(pn >> 8)
	nonce[11] ^= byte(pn)
	ciphertext := gcm.Seal(nil, nonce, plain, header)
	out := append(append([]byte{}, header...), ciphertext...)

	// защита заголовка
	sample := out[pnOffset+4 : pnOffset+4+quicTagLen]
	mask := aesECBFirst5(hp, sample)
	out[0] ^= mask[0] & 0x0f
	for i := 0; i < pnLen; i++ {
		out[pnOffset+i] ^= mask[1+i]
	}
	return out
}

// appendVarint2 дописывает QUIC varint в 2-байтовой форме.
func appendVarint2(b []byte, v uint64) []byte {
	return append(b, byte(0x40|(v>>8)&0x3f), byte(v))
}

func TestParseQUICInitial(t *testing.T) {
	dg := buildQUICInitial("quic.example.com")
	crypto, off, ok := ParseQUICInitial(dg)
	if !ok {
		t.Fatal("QUIC Initial не расшифровался")
	}
	if off != 0 {
		t.Errorf("смещение CRYPTO: %d, хотим 0", off)
	}
	host, isCH := ClientHelloSNI(crypto)
	if !isCH {
		t.Fatal("ClientHello из QUIC не распознан")
	}
	if host != "quic.example.com" {
		t.Errorf("SNI: %q, хотим quic.example.com", host)
	}
}

func TestParseQUICInitialNotInitial(t *testing.T) {
	dg := buildQUICInitial("quic.example.com")
	dg[0] = (dg[0] &^ 0x30) | 0x20 // «Handshake» вместо Initial
	if _, _, ok := ParseQUICInitial(dg); ok {
		t.Error("Handshake-пакет не должен расшифровываться как Initial")
	}
	if _, _, ok := ParseQUICInitial(nil); ok {
		t.Error("пустой пакет не должен разбираться")
	}
}

func TestQUICDetectorTimeout(t *testing.T) {
	d := NewQUICDetector(5 * time.Second)
	now := time.Unix(1000, 0)
	d.SetNow(func() time.Time { return now })
	var events []Event
	d.OnEvent = func(e Event) { events = append(events, e) }

	d.OnDatagram(UDPPacket{
		SrcIP: []byte{192, 168, 0, 2}, DstIP: []byte{93, 184, 216, 34},
		SrcPort: 55555, DstPort: 443,
		Payload: buildQUICInitial("quic.example.com"),
	}, true)
	if d.ActiveFlows() != 1 {
		t.Fatalf("потоков: %d, хотим 1", d.ActiveFlows())
	}
	now = now.Add(6 * time.Second)
	d.Tick()
	if len(events) != 1 || events[0].Kind != KindTimeout || events[0].Domain != "quic.example.com" {
		t.Fatalf("ожидали таймаут quic.example.com, получили %+v", events)
	}
}

func TestQUICDetectorHealthy(t *testing.T) {
	d := NewQUICDetector(5 * time.Second)
	now := time.Unix(1000, 0)
	d.SetNow(func() time.Time { return now })
	var events []Event
	d.OnEvent = func(e Event) { events = append(events, e) }

	out := UDPPacket{SrcIP: []byte{192, 168, 0, 2}, DstIP: []byte{93, 184, 216, 34},
		SrcPort: 55555, DstPort: 443, Payload: buildQUICInitial("quic.example.com")}
	d.OnDatagram(out, true)
	in := UDPPacket{SrcIP: out.DstIP, DstIP: out.SrcIP, SrcPort: 443, DstPort: 55555,
		Payload: []byte{0x40, 0x00, 0x01}}
	d.OnDatagram(in, false)
	if len(events) != 1 || events[0].Kind != KindHealthy {
		t.Fatalf("ожидали «здоров», получили %+v", events)
	}
	now = now.Add(10 * time.Second)
	d.Tick()
	if len(events) != 1 {
		t.Fatalf("после ответа таймаута быть не должно: %+v", events)
	}
}

func TestQUICVarint(t *testing.T) {
	cases := []struct {
		in   []byte
		want uint64
		n    int
		ok   bool
	}{
		{[]byte{0x3f}, 63, 1, true},
		{[]byte{0x40, 0x7f}, 127, 2, true},
		{[]byte{0x80, 0x00, 0x3f, 0xff}, 16383, 4, true},
		{[]byte{}, 0, 0, false},
	}
	for _, c := range cases {
		got, n, ok := quicVarint(c.in)
		if ok != c.ok || (ok && (got != c.want || n != c.n)) {
			t.Errorf("quicVarint(%x) = (%d,%d,%v), хотим (%d,%d,%v)", c.in, got, n, ok, c.want, c.n, c.ok)
		}
	}
}

func TestParseUDPPacket(t *testing.T) {
	ip := make([]byte, 20+8+4)
	ip[0] = 0x45
	ip[9] = 17
	copy(ip[12:16], []byte{192, 168, 0, 2})
	copy(ip[16:20], []byte{93, 184, 216, 34})
	binary.BigEndian.PutUint16(ip[20:22], 55555)
	binary.BigEndian.PutUint16(ip[22:24], 443)
	binary.BigEndian.PutUint16(ip[24:26], 12)
	copy(ip[28:], []byte{1, 2, 3, 4})
	p, ok := ParseUDPPacket(ip)
	if !ok {
		t.Fatal("UDP не разобран")
	}
	if p.SrcPort != 55555 || p.DstPort != 443 || len(p.Payload) != 4 {
		t.Fatalf("неверный разбор: %+v", p)
	}
	if _, ok := ParsePacket(ip); ok {
		t.Error("ParsePacket не должен принимать UDP")
	}
}
