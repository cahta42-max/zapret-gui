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

// ProcessResolver — определяет процесс по локальному сокету соединения.
// Нужен, чтобы в журнале было видно, кто именно ломался: chrome.exe,
// telegram.exe или какой-то фоновый апдейтер.
type ProcessResolver interface {
	Lookup(localIP string, localPort uint16) (pid uint32, name string)
}

const (
	tcpTableOwnerPidAll = 5
	afInet              = 2
	afInet6             = 23

	tcpRow4Size = 24 // MIB_TCPROW_OWNER_PID
	tcpRow6Size = 56 // MIB_TCP6ROW_OWNER_PID
)

var (
	modIphlpapi             = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTcpTable = modIphlpapi.NewProc("GetExtendedTcpTable")
)

// tcpResolver — резолвер по TCP-таблице Windows: сокет → PID, PID → имя.
type tcpResolver struct {
	mu       sync.Mutex
	bySocket map[string]uint32 // "ip:порт" → pid
	builtAt  time.Time
	names    map[uint32]string
	namesAt  time.Time
}

// NewProcessResolver — резолвер процессов для детектора (Windows).
func NewProcessResolver() ProcessResolver {
	return &tcpResolver{bySocket: map[string]uint32{}, names: map[uint32]string{}}
}

// Lookup возвращает PID и имя процесса по локальному адресу соединения.
func (r *tcpResolver) Lookup(localIP string, localPort uint16) (uint32, string) {
	if localIP == "" || localPort == 0 {
		return 0, ""
	}
	key := localIP + ":" + strconv.Itoa(int(localPort))
	r.mu.Lock()
	if len(r.bySocket) == 0 || time.Since(r.builtAt) > 500*time.Millisecond {
		r.refreshSocketsLocked()
	}
	pid := r.bySocket[key]
	if pid == 0 && time.Since(r.builtAt) > 100*time.Millisecond {
		// сокет мог появиться уже после сборки кэша — обновим один раз
		r.refreshSocketsLocked()
		pid = r.bySocket[key]
	}
	if pid == 0 {
		r.mu.Unlock()
		return 0, ""
	}
	if len(r.names) == 0 || time.Since(r.namesAt) > 10*time.Second {
		r.refreshNamesLocked()
	}
	name := r.names[pid]
	r.mu.Unlock()
	return pid, name
}

func (r *tcpResolver) refreshSocketsLocked() {
	sockets := make(map[string]uint32, 512)
	for _, fam := range []uint32{afInet, afInet6} {
		buf, err := tcpTable(fam)
		if err != nil {
			continue
		}
		for k, v := range ParseTCPTable(fam, buf) {
			sockets[k] = v
		}
	}
	r.bySocket = sockets
	r.builtAt = time.Now()
}

// refreshNamesLocked — имена процессов одним снимком (дешевле, чем
// OpenProcess на каждый пакет).
func (r *tcpResolver) refreshNamesLocked() {
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
	var size uint32
	procGetExtendedTcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0,
		uintptr(family), tcpTableOwnerPidAll, 0)
	if size == 0 {
		return nil, syscall.EINVAL
	}
	buf := make([]byte, size)
	r, _, _ := procGetExtendedTcpTable.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0,
		uintptr(family), tcpTableOwnerPidAll, 0)
	if r != 0 {
		return nil, syscall.Errno(r)
	}
	return buf, nil
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
