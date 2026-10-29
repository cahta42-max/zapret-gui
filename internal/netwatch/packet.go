// Package netwatch — пассивное наблюдение за TLS-соединениями браузера.
//
// Сниффер (WinDivert, режим «только смотреть») отдаёт IP-пакеты, детектор
// находит в ClientHello имя сайта (SNI) и ждёт ответа сервера. Если сервер
// не ответил за timeout или прислал RST — это событие «сайт ломается»,
// кандидат на автолечение (маскировать/исключить). Политика решает, когда
// из серии сбоев пора реально что-то менять.
//
// Пакет намеренно не знает ни про zapret, ни про списки доменов: только
// «что увидели» → «что решили» — это позволяет покрыть всю логику тестами
// на синтетических пакетах.
package netwatch

import (
	"encoding/binary"
	"net"
)

// TCP-флаги (минимальный набор, который нас интересует).
const (
	FlagFIN uint8 = 0x01
	FlagSYN uint8 = 0x02
	FlagRST uint8 = 0x04
	FlagPSH uint8 = 0x08
	FlagACK uint8 = 0x10
)

// Packet — то, что нужно детектору от пакета: адреса, флаги и payload.
type Packet struct {
	SrcIP   net.IP
	DstIP   net.IP
	SrcPort uint16
	DstPort uint16
	Seq     uint32 // TCP sequence number: нужен для сборки разрезанного ClientHello
	Flags   uint8
	Payload []byte
}

// ParsePacket разбирает IPv4/IPv6 пакет с TCP внутри.
// ok=false — не TCP, обрезанный или битый пакет (такие просто игнорируем).
func ParsePacket(b []byte) (Packet, bool) {
	if len(b) < 20 {
		return Packet{}, false
	}
	var p Packet
	var off int
	switch b[0] >> 4 {
	case 4:
		ihl := int(b[0]&0x0f) * 4
		if ihl < 20 || len(b) < ihl+20 || b[9] != 6 {
			return Packet{}, false
		}
		p.SrcIP = net.IPv4(b[12], b[13], b[14], b[15])
		p.DstIP = net.IPv4(b[16], b[17], b[18], b[19])
		off = ihl
	case 6:
		// TCP сразу за 40-байтовым заголовком: пакеты с extension headers
		// (Hop-by-Hop и т.п.) не разбираем — для HTTP(S)-трафика это редкость
		if len(b) < 60 || b[6] != 6 {
			return Packet{}, false
		}
		p.SrcIP = net.IP(append([]byte(nil), b[8:24]...))
		p.DstIP = net.IP(append([]byte(nil), b[24:40]...))
		off = 40
	default:
		return Packet{}, false
	}
	tcp := b[off:]
	if len(tcp) < 20 {
		return Packet{}, false
	}
	p.SrcPort = binary.BigEndian.Uint16(tcp[0:2])
	p.DstPort = binary.BigEndian.Uint16(tcp[2:4])
	p.Seq = binary.BigEndian.Uint32(tcp[4:8])
	dataOff := int(tcp[12]>>4) * 4
	if dataOff < 20 || len(tcp) < dataOff {
		return Packet{}, false
	}
	p.Flags = tcp[13]
	if len(tcp) > dataOff {
		p.Payload = tcp[dataOff:]
	}
	return p, true
}
