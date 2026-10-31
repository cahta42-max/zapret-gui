//go:build windows

package netwatch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const (
	windivertLayerNetwork = 0
	windivertFlagSniff    = 0x0001
	windivertFlagRecvOnly = 0x0004
	windivertShutdownBoth = 3
	invalidHandle         = ^uintptr(0)

	packetBufSize = 65535 // больше пакета не бывает
	addrBufSize   = 128   // WINDIVERT_ADDRESS (union flow/socket больше 24 байт)
)

// DefaultFilter — что слушаем: TCP на типичных HTTP(S)-портах и QUIC
// (UDP/443), в обе стороны; пакеты с данными и RST (RST от DPI приходит без
// payload).
const DefaultFilter = `(tcp and ((tcp.PayloadLength > 0) or (tcp.Rst)) and (` +
	`tcp.DstPort == 443 or tcp.SrcPort == 443 or ` +
	`tcp.DstPort == 80 or tcp.SrcPort == 80 or ` +
	`tcp.DstPort == 8443 or tcp.SrcPort == 8443 or ` +
	`tcp.DstPort == 8080 or tcp.SrcPort == 8080)) or ` +
	`(udp and (udp.PayloadLength > 0) and ` +
	`(udp.DstPort == 443 or udp.SrcPort == 443))`

var (
	dllMu      sync.Mutex
	dllLoaded  bool
	dllErr     error
	libMod     *syscall.LazyDLL
	procOpen   *syscall.LazyProc
	procRecv   *syscall.LazyProc
	procShutdn *syscall.LazyProc
	procClose  *syscall.LazyProc
)

// loadDLL подгружает WinDivert.dll из дистрибутива zapret (свой DLL не тащим).
func loadDLL(dir string) error {
	dllMu.Lock()
	defer dllMu.Unlock()
	if dllLoaded {
		return dllErr
	}
	dllLoaded = true
	path := filepath.Join(dir, "WinDivert.dll")
	mod := syscall.NewLazyDLL(path)
	if err := mod.Load(); err != nil {
		dllErr = fmt.Errorf("WinDivert.dll не загрузилась (%s): %w", path, err)
		return dllErr
	}
	libMod = mod
	procOpen = mod.NewProc("WinDivertOpen")
	procRecv = mod.NewProc("WinDivertRecv")
	procShutdn = mod.NewProc("WinDivertShutdown")
	procClose = mod.NewProc("WinDivertClose")
	return nil
}

// Unload выгружает WinDivert.dll из процесса. Нужно после обновления
// дистрибутива zapret: файлы на диске заменяются, и старый DLL в памяти
// перестаёт соответствовать новому драйверу (WinDivertOpen начинает
// возвращать «device does not exist»). Перед вызовом закройте сниффер.
func Unload() {
	dllMu.Lock()
	defer dllMu.Unlock()
	if !dllLoaded {
		return
	}
	if libMod != nil {
		_ = syscall.FreeLibrary(syscall.Handle(libMod.Handle()))
	}
	libMod = nil
	procOpen, procRecv, procShutdn, procClose = nil, nil, nil, nil
	dllLoaded = false
	dllErr = nil
}

// Sniffer — пассивный сниффер WinDivert (WINDIVERT_FLAG_SNIFF|RECV_ONLY):
// копия пакетов, трафик не задерживается и не меняется.
type Sniffer struct {
	handle uintptr
	buf    []byte
	closed int32
}

// OpenSniffer открывает сниффер. dllDir — каталог с WinDivert.dll
// (у нас <tools>/zapret/bin). Требует прав администратора (как и winws).
func OpenSniffer(dllDir, filter string) (*Sniffer, error) {
	if err := loadDLL(dllDir); err != nil {
		return nil, err
	}
	f, err := syscall.BytePtrFromString(filter)
	if err != nil {
		return nil, err
	}
	r, _, callErr := procOpen.Call(
		uintptr(unsafe.Pointer(f)),
		uintptr(windivertLayerNetwork),
		0,
		uintptr(windivertFlagSniff|windivertFlagRecvOnly),
	)
	if r == invalidHandle {
		if callErr == nil || callErr == syscall.Errno(0) {
			return nil, errors.New("WinDivertOpen: не удалось открыть сниффер")
		}
		switch callErr {
		case syscall.ERROR_ACCESS_DENIED:
			return nil, fmt.Errorf("WinDivertOpen: нужны права администратора (%v)", callErr)
		case syscall.Errno(87): // ERROR_INVALID_PARAMETER
			return nil, fmt.Errorf("WinDivertOpen: неверный фильтр или флаги (%v)", callErr)
		}
		return nil, fmt.Errorf("WinDivertOpen: %v", callErr)
	}
	return &Sniffer{handle: r, buf: make([]byte, packetBufSize)}, nil
}

// Run читает пакеты и отдаёт их в onPacket до Close/ошибки.
// outbound=true — пакет ушёл от нас; false — пришёл из сети.
func (s *Sniffer) Run(onPacket func(outbound bool, raw []byte)) error {
	addr := make([]byte, addrBufSize)
	recvLen := uint32(0)
	for atomic.LoadInt32(&s.closed) == 0 {
		r, _, callErr := procRecv.Call(
			s.handle,
			uintptr(unsafe.Pointer(&s.buf[0])),
			uintptr(len(s.buf)),
			uintptr(unsafe.Pointer(&recvLen)),
			uintptr(unsafe.Pointer(&addr[0])),
		)
		if r == 0 {
			if atomic.LoadInt32(&s.closed) != 0 {
				return nil // штатная остановка через Close
			}
			return fmt.Errorf("WinDivertRecv: %v", callErr)
		}
		if recvLen == 0 || int(recvLen) > len(s.buf) {
			continue
		}
		// WINDIVERT_ADDRESS: INT64 Timestamp, затем битовое поле по смещению 8
		// (Layer:8, Event:8, Sniffed:1, Outbound:1, ...); Outbound = бит 17 = 0x02 в байте 10
		outbound := addr[10]&0x02 != 0
		onPacket(outbound, s.buf[:recvLen])
	}
	return nil
}

// Close останавливает Run и освобождает хэндл. Повторный вызов безопасен.
func (s *Sniffer) Close() {
	if !atomic.CompareAndSwapInt32(&s.closed, 0, 1) {
		return
	}
	if procShutdn != nil {
		procShutdn.Call(s.handle, windivertShutdownBoth)
	}
	if procClose != nil {
		procClose.Call(s.handle)
	}
}

// RunWithContext — удобная обёртка: Run до отмены ctx, затем Close.
func (s *Sniffer) RunWithContext(ctx context.Context, onPacket func(outbound bool, raw []byte)) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-done:
		}
	}()
	err := s.Run(onPacket)
	close(done)
	return err
}
