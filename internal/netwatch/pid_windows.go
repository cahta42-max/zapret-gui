//go:build windows

package netwatch

import (
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Protocol — транспорт для поиска процесса по сокету.
type Protocol int

const (
	ProtoTCP Protocol = iota
	ProtoUDP
)

// ProcessResolver — определяет процесс по локальному сокету соединения.
// Нужен, чтобы в журнале было видно, кто именно ломался: chrome.exe,
// telegram.exe или какой-то фоновый апдейтер.
type ProcessResolver interface {
	Lookup(proto Protocol, localIP string, localPort uint16) (pid uint32, name string)
}

const (
	tcpTableOwnerPidAll = 5
	udpTableOwnerPidAll = 1
	afInet              = 2
	afInet6             = 23

	tcpRow4Size = 24 // MIB_TCPROW_OWNER_PID
	tcpRow6Size = 56 // MIB_TCP6ROW_OWNER_PID
	udpRow4Size = 12 // MIB_UDPROW_OWNER_PID
	udpRow6Size = 28 // MIB_UDP6ROW_OWNER_PID
)

var (
	modIphlpapi             = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modIphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUdpTable = modIphlpapi.NewProc("GetExtendedUdpTable")
)

// socketResolver — резолвер по таблицам Windows: сокет → PID, PID → имя.
type socketResolver struct {
	mu      sync.Mutex
	tcp     map[string]uint32 // "ip:порт" → pid
	udp     map[string]uint32
	builtAt time.Time
	names   map[uint32]string
	namesAt time.Time
}

// NewProcessResolver — резолвер процессов для детекторов (Windows).
func NewProcessResolver() ProcessResolver {
	return &socketResolver{
		tcp:   map[string]uint32{},
		udp:   map[string]uint32{},
		names: map[uint32]string{},
	}
}

// Lookup возвращает PID и имя процесса по локальному адресу сокета.
func (r *socketResolver) Lookup(proto Protocol, localIP string, localPort uint16) (uint32, string) {
	if localIP == "" || localPort == 0 {
		return 0, ""
	}
	key := localIP + ":" + strconv.Itoa(int(localPort))
	wildcard := wildcardKey(localIP, localPort)
	r.mu.Lock()
	if len(r.tcp) == 0 || time.Since(r.builtAt) > 500*time.Millisecond {
		r.refreshSocketsLocked()
	}
	table := r.tcp
	if proto == ProtoUDP {
		table = r.udp
	}
	find := func(t map[string]uint32) uint32 {
		if pid := t[key]; pid != 0 {
			return pid
		}
		// UDP-сокеты у DialUDP привязаны к 0.0.0.0/::, а пакет идёт с
		// конкретного адреса интерфейса — ищем ещё и по wildcard-ключу
		if wildcard != "" {
			return t[wildcard]
		}
		return 0
	}
	pid := find(table)
	if pid == 0 {
		// сокет мог появиться уже после сборки кэша (новое соединение) —
		// при промахе обновляем таблицы один раз и пробуем снова
		r.refreshSocketsLocked()
		table = r.tcp
		if proto == ProtoUDP {
			table = r.udp
		}
		pid = find(table)
	}
	if pid == 0 {
		r.mu.Unlock()
		return 0, ""
	}
	if len(r.names) == 0 || time.Since(r.namesAt) > 10*time.Second {
		r.refreshNamesLocked()
	}
	name := r.names[pid]
	if name == "" {
		// процесс появился после сборки снимка имён — обновим и повторим
		r.refreshNamesLocked()
		name = r.names[pid]
	}
	r.mu.Unlock()
	return pid, name
}

func (r *socketResolver) refreshSocketsLocked() {
	tcpSockets := make(map[string]uint32, 512)
	udpSockets := make(map[string]uint32, 256)
	for _, fam := range []uint32{afInet, afInet6} {
		if buf, err := tcpTable(fam); err == nil {
			for k, v := range ParseTCPTable(fam, buf) {
				tcpSockets[k] = v
			}
		}
		if buf, err := udpTable(fam); err == nil {
			for k, v := range ParseUDPTable(fam, buf) {
				udpSockets[k] = v
			}
		}
	}
	r.tcp = tcpSockets
	r.udp = udpSockets
	r.builtAt = time.Now()
}

// wildcardKey — ключ таблицы для сокета, привязанного к 0.0.0.0/::.
func wildcardKey(localIP string, localPort uint16) string {
	ip := net.ParseIP(localIP)
	if ip == nil {
		return ""
	}
	port := strconv.Itoa(int(localPort))
	if ip.To4() != nil {
		return "0.0.0.0:" + port
	}
	return "::" + ":" + port
}

// refreshNamesLocked — имена процессов одним снимком (дешевле, чем
// OpenProcess на каждый пакет).
func (r *socketResolver) refreshNamesLocked() {
	names := make(map[uint32]string, 256)
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err == nil {
		defer windows.CloseHandle(snap)
		var e windows.ProcessEntry32
		e.Size = uint32(unsafe.Sizeof(e))
		for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
			names[e.ProcessID] = windows.UTF16ToString(e.ExeFile[:])
		}
	}
	r.names = names
	r.namesAt = time.Now()
}

// tcpTable — сырая таблица TCP-соединений с PID владельца.
func tcpTable(family uint32) ([]byte, error) {
	return socketTable(procGetExtendedTcpTable, family, tcpTableOwnerPidAll, 4)
}

// socketTable — общий вызов GetExtended*Table с повтором на случай, если
// размер таблицы изменился между запросами.
func socketTable(proc *syscall.LazyProc, family, class uint32, header uint32) ([]byte, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var size uint32
		proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, uintptr(family), uintptr(class), 0)
		if size == 0 {
			return nil, syscall.EINVAL
		}
		buf := make([]byte, size)
		r, _, _ := proc.Call(
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
			uintptr(family), uintptr(class), 0)
		if r == 0 {
			return buf, nil
		}
		if syscall.Errno(r) != syscall.ERROR_INSUFFICIENT_BUFFER {
			return nil, syscall.Errno(r)
		}
	}
	return nil, syscall.ERROR_INSUFFICIENT_BUFFER
}

// udpTable — сырая таблица UDP-сокетов с PID владельца.
func udpTable(family uint32) ([]byte, error) {
	return socketTable(procGetExtendedUdpTable, family, udpTableOwnerPidAll, 4)
}

// ParseUDPTable разбирает MIB_UDPTABLE_OWNER_PID (IPv4) или
// MIB_UDP6TABLE_OWNER_PID (IPv6) в карту "ip:порт" → PID.
func ParseUDPTable(family uint32, buf []byte) map[string]uint32 {
	out := make(map[string]uint32)
	if len(buf) < 4 {
		return out
	}
	num := binary.LittleEndian.Uint32(buf[0:4])
	rowSize := udpRow4Size
	if family == afInet6 {
		rowSize = udpRow6Size
	}
	off := 4
	for i := uint32(0); i < num; i++ {
		if off+rowSize > len(buf) {
			break
		}
		row := buf[off : off+rowSize]
		off += rowSize
		var ip net.IP
		var rawPort, pid uint32
		if family == afInet6 {
			ip = net.IP(append([]byte(nil), row[0:16]...))
			rawPort = binary.LittleEndian.Uint32(row[20:24])
			pid = binary.LittleEndian.Uint32(row[24:28])
		} else {
			ip = net.IPv4(row[0], row[1], row[2], row[3])
			rawPort = binary.LittleEndian.Uint32(row[4:8])
			pid = binary.LittleEndian.Uint32(row[8:12])
		}
		port := uint16(rawPort&0xFF)<<8 | uint16((rawPort>>8)&0xFF)
		if port == 0 || pid == 0 {
			continue
		}
		out[ip.String()+":"+strconv.Itoa(int(port))] = pid
	}
	return out
}

// ParseTCPTable разбирает MIB_TCPTABLE_OWNER_PID (IPv4) или
// MIB_TCP6TABLE_OWNER_PID (IPv6) в карту "ip:порт" → PID.
// Вынесено отдельно от WinAPI, чтобы покрыть тестами.
func ParseTCPTable(family uint32, buf []byte) map[string]uint32 {
	out := make(map[string]uint32)
	if len(buf) < 4 {
		return out
	}
	num := binary.LittleEndian.Uint32(buf[0:4])
	rowSize := tcpRow4Size
	if family == afInet6 {
		rowSize = tcpRow6Size
	}
	off := 4
	for i := uint32(0); i < num; i++ {
		if off+rowSize > len(buf) {
			break
		}
		row := buf[off : off+rowSize]
		off += rowSize
		var ip net.IP
		var rawPort uint32
		var pid uint32
		if family == afInet6 {
			ip = net.IP(append([]byte(nil), row[0:16]...))
			if ip.IsUnspecified() {
				continue
			}
			rawPort = binary.LittleEndian.Uint32(row[20:24])
			pid = binary.LittleEndian.Uint32(row[52:56])
		} else {
			ip = net.IPv4(row[4], row[5], row[6], row[7])
			if ip.Equal(net.IPv4zero) {
				continue
			}
			rawPort = binary.LittleEndian.Uint32(row[8:12])
			pid = binary.LittleEndian.Uint32(row[20:24])
		}
		port := uint16(rawPort&0xFF)<<8 | uint16((rawPort>>8)&0xFF)
		if port == 0 || pid == 0 {
			continue
		}
		out[ip.String()+":"+strconv.Itoa(int(port))] = pid
	}
	return out
}
