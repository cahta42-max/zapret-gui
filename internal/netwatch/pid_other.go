//go:build !windows

package netwatch

// ProcessResolver — определяет процесс по локальному сокету соединения.
// На не-Windows платформах определение процессов не реализовано.
type ProcessResolver interface {
	Lookup(localIP string, localPort uint16) (pid uint32, name string)
}

// NewProcessResolver — на не-Windows процессов не знаем.
func NewProcessResolver() ProcessResolver { return nil }
