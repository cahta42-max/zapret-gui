//go:build windows

package netwatch

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// putPort пишет порт так, как его хранит Windows: network byte order в DWORD
func putPort(row []byte, off int, port uint16) {
	row[off] = byte(port >> 8)
	row[off+1] = byte(port)
}

func buildTCP4(rows ...[6]uint32) []byte {
	buf := make([]byte, 4+len(rows)*tcpRow4Size)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(rows)))
	off := 4
	for _, r := range rows {
		row := buf[off : off+tcpRow4Size]
		binary.LittleEndian.PutUint32(row[0:4], r[0])   // state
		binary.LittleEndian.PutUint32(row[4:8], r[1])   // local addr (сетевой порядок байт)
		putPort(row, 8, uint16(r[2]))                   // local port
		binary.LittleEndian.PutUint32(row[12:16], r[3]) // remote addr
		putPort(row, 16, uint16(r[4]))                  // remote port
		binary.LittleEndian.PutUint32(row[20:24], r[5]) // pid
		off += tcpRow4Size
	}
	return buf
}

func TestParseTCPTableV4(t *testing.T) {
	// 192.168.0.10:54321 → pid 1234; 10.0.0.1:443 → pid 5678
	// адреса в таблице лежат в сетевом порядке байт
	ip1 := binary.LittleEndian.Uint32([]byte{192, 168, 0, 10})
	ip2 := binary.LittleEndian.Uint32([]byte{10, 0, 0, 1})
	rows := [][6]uint32{
		{2, ip1, 54321, 0, 0, 1234}, // состояние LISTEN? неважно, берём строку
		{5, ip2, 443, 0, 0, 5678},
		{2, 0, 0, 0, 0, 999}, // пустой адрес — пропускаем
	}
	buf := buildTCP4(rows...)
	got := ParseTCPTable(afInet, buf)
	if got["192.168.0.10:54321"] != 1234 {
		t.Errorf("нет PID для 192.168.0.10:54321: %v", got)
	}
	if got["10.0.0.1:443"] != 5678 {
		t.Errorf("нет PID для 10.0.0.1:443: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("ожидали 2 записи, получили %d: %v", len(got), got)
	}
}

func TestParseTCPTableV4BadBuffer(t *testing.T) {
	if len(ParseTCPTable(afInet, nil)) != 0 {
		t.Error("nil-буфер должен давать пустую карту")
	}
	// заголовок обещает 5 строк, а данных нет — не паникуем
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, 5)
	if len(ParseTCPTable(afInet, buf)) != 0 {
		t.Error("обрезанный буфер должен давать пустую карту")
	}
}

func TestParseTCPTableV6(t *testing.T) {
	// ::1:5555 → pid 42
	buf := make([]byte, 4+tcpRow6Size)
	binary.LittleEndian.PutUint32(buf[0:4], 1)
	row := buf[4:]
	row[15] = 1 // ::1
	putPort(row, 20, 5555)
	binary.LittleEndian.PutUint32(row[52:56], 42)
	got := ParseTCPTable(afInet6, buf)
	if got["::1:5555"] != 42 {
		t.Errorf("нет PID для ::1:5555: %v", got)
	}
}

func TestTCPResolverStructSize(t *testing.T) {
	// защита от перепутанных размеров строк таблиц
	if unsafe.Sizeof([tcpRow4Size]byte{}) != 24 {
		t.Fatal("tcpRow4Size")
	}
}

func TestParseUDPTableV4(t *testing.T) {
	// 0.0.0.0:5353 слушает pid 777
	buf := make([]byte, 4+udpRow4Size)
	binary.LittleEndian.PutUint32(buf[0:4], 1)
	row := buf[4:]
	row[0], row[1], row[2], row[3] = 0, 0, 0, 0
	putPort(row, 4, 5353)
	binary.LittleEndian.PutUint32(row[8:12], 777)
	got := ParseUDPTable(afInet, buf)
	if got["0.0.0.0:5353"] != 777 {
		t.Errorf("нет PID для 0.0.0.0:5353: %v", got)
	}
}

func TestParseUDPTableV6(t *testing.T) {
	// ::1:5353 → pid 778
	buf := make([]byte, 4+udpRow6Size)
	binary.LittleEndian.PutUint32(buf[0:4], 1)
	row := buf[4:]
	row[15] = 1
	putPort(row, 20, 5353)
	binary.LittleEndian.PutUint32(row[24:28], 778)
	got := ParseUDPTable(afInet6, buf)
	if got["::1:5353"] != 778 {
		t.Errorf("нет PID для ::1:5353: %v", got)
	}
}

func TestWildcardKey(t *testing.T) {
	if got := wildcardKey("192.168.0.81", 51165); got != "0.0.0.0:51165" {
		t.Errorf("v4 wildcard: %q", got)
	}
	if got := wildcardKey("::1", 51165); got != ":::51165" {
		t.Errorf("v6 wildcard: %q", got)
	}
	if got := wildcardKey("мусор", 1); got != "" {
		t.Errorf("невалидный IP: %q", got)
	}
}
