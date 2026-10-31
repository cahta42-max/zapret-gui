// Package engine управляет движком DPI-обхода SpoofDPI на macOS:
// установка из GitHub-релизов, профили-«стратегии», запуск/остановка
// (в т.ч. от root — для fake-пакетов нужны raw-сокеты), версия.
package engine

// Profile — профиль запуска spoofdpi («стратегия» в терминах GUI).
type Profile struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"` // имя для списка стратегий в GUI
	Args      []string // дополнительные флаги поверх базовых
	NeedsRoot bool     // fake/disorder требуют raw-сокеты → root
}

// BaseArgs — общие флаги для любого профиля.
// --clean игнорирует пользовательские конфиги spoofdpi (детерминизм),
// --no-tui отключает терминальный интерфейс (мы — GUI-обёртка),
// порт подставляется менеджером (см. Manager.Start).
func BaseArgs(port int) []string {
	return []string{
		"--clean",
		"--no-tui",
		"--app-mode", "http",
		"--listen-addr", "127.0.0.1:" + itoa(port),
	}
}

// Profiles — доступные профили (порядок = порядок в GUI и в полном тесте).
func Profiles() []Profile {
	return []Profile{
		{ID: "split-sni", Title: "Стандарт (split SNI)"},
		{ID: "chunk-35", Title: "Split по размеру (chunk 35)", Args: []string{"--https-split-mode", "chunk", "--https-chunk-size", "35"}},
		{ID: "fake-1", Title: "Fake-пакеты ×1", Args: []string{"--https-fake-count", "1"}, NeedsRoot: true},
		{ID: "fake-doh", Title: "Fake ×2 + DNS over HTTPS", Args: []string{"--https-fake-count", "2", "--dns-mode", "doh"}, NeedsRoot: true},
		{ID: "disorder-fake", Title: "Disorder + Fake ×1", Args: []string{"--https-disorder", "--https-fake-count", "1"}, NeedsRoot: true},
		{ID: "udp-fake", Title: "Fake QUIC (UDP ×1)", Args: []string{"--udp-fake-count", "1"}, NeedsRoot: true},
	}
}

// FindProfile ищет профиль по ID.
func FindProfile(id string) (Profile, bool) {
	for _, p := range Profiles() {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

// DefaultProfileID — профиль по умолчанию (без root).
const DefaultProfileID = "split-sni"

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	if neg {
		return "-" + digits
	}
	return digits
}
