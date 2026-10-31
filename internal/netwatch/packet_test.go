package netwatch

import (
	"encoding/binary"
	"net"
	"testing"
)

func ipv4TCP(t *testing.T, src, dst string, sport, dport uint16, flags uint8, payload []byte) []byte {
	return ipv4TCPSeq(t, src, dst, sport, dport, 0, flags, payload)
}

func ipv4TCPSeq(t *testing.T, src, dst string, sport, dport uint16, seq uint32, flags uint8, payload []byte) []byte {
	t.Helper()
	sip, dip := net.ParseIP(src).To4(), net.ParseIP(dst).To4()
	if sip == nil || dip == nil {
		t.Fatalf("плохие адреса: %q / %q", src, dst)
	}
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:2], sport)
	binary.BigEndian.PutUint16(tcp[2:4], dport)
	binary.BigEndian.PutUint32(tcp[4:8], seq)
	tcp[12] = 0x50 // data offset = 5
	tcp[13] = flags

	pkt := make([]byte, 20)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(20+len(tcp)+len(payload)))
	pkt[8] = 64
	pkt[9] = 6 // TCP
	copy(pkt[12:16], sip)
	copy(pkt[16:20], dip)
	pkt = append(pkt, tcp...)
	return append(pkt, payload...)
}

func ipv6TCP(t *testing.T, src, dst string, sport, dport uint16, flags uint8, payload []byte) []byte {
	t.Helper()
	sip, dip := net.ParseIP(src).To16(), net.ParseIP(dst).To16()
	if sip == nil || dip == nil {
		t.Fatalf("плохие адреса: %q / %q", src, dst)
	}
	tcp := make([]byte, 20)
	binary.BigEndian.PutUint16(tcp[0:2], sport)
	binary.BigEndian.PutUint16(tcp[2:4], dport)
	tcp[12] = 0x50
	tcp[13] = flags

	pkt := make([]byte, 40)
	pkt[0] = 0x60
	binary.BigEndian.PutUint16(pkt[4:6], uint16(20+len(payload)))
	pkt[6] = 6 // next header: TCP
	pkt[7] = 64
	copy(pkt[8:24], sip)
	copy(pkt[24:40], dip)
	pkt = append(pkt, tcp...)
	return append(pkt, payload...)
}

func TestParsePacketIPv4(t *testing.T) {
	raw := ipv4TCP(t, "10.0.0.5", "203.0.113.10", 51234, 443, FlagPSH|FlagACK, []byte("hello"))
	p, ok := ParsePacket(raw)
	if !ok {
		t.Fatal("пакет не разобрался")
	}
	if p.SrcPort != 51234 || p.DstPort != 443 {
		t.Errorf("порты: %d → %d", p.SrcPort, p.DstPort)
	}
	if p.Flags != FlagPSH|FlagACK {
		t.Errorf("флаги: %#x", p.Flags)
	}
	if string(p.Payload) != "hello" {
		t.Errorf("payload: %q", p.Payload)
	}
	if !p.SrcIP.Equal(net.ParseIP("10.0.0.5")) || !p.DstIP.Equal(net.ParseIP("203.0.113.10")) {
		t.Errorf("адреса: %v → %v", p.SrcIP, p.DstIP)
	}
}

func TestParsePacketIPv6(t *testing.T) {
	raw := ipv6TCP(t, "2001:db8::1", "2001:db8::2", 40000, 443, FlagACK, []byte("x"))
	p, ok := ParsePacket(raw)
	if !ok {
		t.Fatal("IPv6-пакет не разобрался")
	}
	if p.SrcPort != 40000 || p.DstPort != 443 || string(p.Payload) != "x" {
		t.Errorf("разобрано неверно: %+v", p)
	}
}

func TestParsePacketRejects(t *testing.T) {
	cases := map[string][]byte{
		"пусто":          nil,
		"короткий":       {0x45, 0, 0, 20},
		"не TCP":         ipv4TCP(t, "10.0.0.1", "10.0.0.2", 1, 2, FlagACK, nil)[:20],
		"обрезанный TCP": ipv4TCP(t, "10.0.0.1", "10.0.0.2", 1, 2, FlagACK, nil)[:30],
	}
	for name, raw := range cases {
		if _, ok := ParsePacket(raw); ok {
			t.Errorf("%s: ожидался отказ разбора", name)
		}
	}
}

func TestParsePacketUDPIgnored(t *testing.T) {
	raw := ipv4TCP(t, "10.0.0.1", "10.0.0.2", 53, 53, 0, nil)
	raw[9] = 17 // UDP
	if _, ok := ParsePacket(raw); ok {
		t.Fatal("UDP-пакет не должен разбираться как TCP")
	}
}
