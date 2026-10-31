package netwatch

import (
	"encoding/binary"
	"net"
)

// UDPPacket — то, что нужно QUIC-детектору от датаграммы.
type UDPPacket struct {
	SrcIP   net.IP
	DstIP   net.IP
	SrcPort uint16
	DstPort uint16
	Payload []byte
}

// ParseUDPPacket разбирает IPv4/IPv6 пакет с UDP внутри.
// ok=false — не UDP, обрезанный или битый пакет.
func ParseUDPPacket(b []byte) (UDPPacket, bool) {
	if len(b) < 20 {
		return UDPPacket{}, false
	}
	var p UDPPacket
	var off int
	switch b[0] >> 4 {
	case 4:
		ihl := int(b[0]&0x0f) * 4
		if ihl < 20 || len(b) < ihl+8 || b[9] != 17 {
			return UDPPacket{}, false
		}
		p.SrcIP = net.IPv4(b[12], b[13], b[14], b[15])
		p.DstIP = net.IPv4(b[16], b[17], b[18], b[19])
		off = ihl
	case 6:
		// extension headers не разбираем: QUIC-трафик идёт без них
		if len(b) < 48 || b[6] != 17 {
			return UDPPacket{}, false
		}
		p.SrcIP = net.IP(append([]byte(nil), b[8:24]...))
		p.DstIP = net.IP(append([]byte(nil), b[24:40]...))
		off = 40
	default:
		return UDPPacket{}, false
	}
	udp := b[off:]
	if len(udp) < 8 {
		return UDPPacket{}, false
	}
	p.SrcPort = binary.BigEndian.Uint16(udp[0:2])
	p.DstPort = binary.BigEndian.Uint16(udp[2:4])
	ulen := int(binary.BigEndian.Uint16(udp[4:6]))
	if ulen >= 8 && ulen <= len(udp) {
		p.Payload = udp[8:ulen]
	} else {
		p.Payload = udp[8:]
	}
	return p, true
}
