//go:build darwin

// Package vpndetect на macOS не используется: автопауза при VPN сделана для
// Windows-версии (сетевой движок utunws и другой способ подключения).
package vpndetect

// Info — результат проверки (на macOS всегда «VPN не определён»).
type Info struct {
	Active  bool
	Name    string
	Clients []string
}

// Detect на macOS всегда возвращает «не активен».
func Detect() Info { return Info{} }

// IsVPNProcess — заглушка для единообразия API.
func IsVPNProcess(string) bool { return false }

// IsTunnelAdapter — заглушка для единообразия API.
func IsTunnelAdapter(uint32, string, string) bool { return false }
