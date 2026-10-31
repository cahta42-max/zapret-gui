// Package vpndetect отвечает на вопрос «включён ли сейчас VPN» — через WinAPI,
// без сторонних утилит и без прав администратора.
//
// Активность определяется по сетевому уровню: поднят туннельный адаптер
// (PPP/L2TP/PPTP, TUNNEL, TAP/Wintun/WireGuard/Tailscale/Clash/mihomo и т.п.).
// Просто запущенный клиент без подключения (idle WireGuard) паузой не
// считается — он попадает только в справочное поле Clients для журнала.
package vpndetect

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// ifType из ipifcons.h
	ifTypePPP    = 23  // PPP (L2TP/PPTP/PPP)
	ifTypeTunnel = 131 // TUNNEL (Teredo/ISATAP/6to4 — отсекаются по имени)

	// нужны только имена/типы/состояние, остальное не собираем
	gaaFlags = windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST | windows.GAA_FLAG_SKIP_DNS_SERVER

	ifOperStatusUp = windows.IfOperStatusUp
)

// Info — результат проверки.
type Info struct {
	// Active — VPN реально несёт интернет: маршрут по умолчанию идёт через
	// поднятый туннельный адаптер. Только это состояние повод паузить zapret.
	Active bool
	// Name — имя туннельного адаптера, через который идёт трафик.
	Name string
	// Adapters — все поднятые туннельные адаптеры (справочно; например,
	// Tailscale в режиме mesh поднят, но интернет через него не идёт).
	Adapters []string
	// Clients — запущенные VPN-клиенты (справочно, на Active не влияет).
	Clients []string
}

// TunnelAdapter — поднятый туннельный адаптер.
type TunnelAdapter struct {
	IfIndex uint32
	Name    string
	Desc    string
	IfType  uint32
}

// Detect — проверка состояния VPN.
func Detect() Info {
	var info Info
	info.Clients = ClientProcesses()
	adapters, err := tunnelAdapters()
	if err != nil || len(adapters) == 0 {
		return info
	}
	for _, a := range adapters {
		name := a.Name
		if name == "" {
			name = a.Desc
		}
		info.Adapters = append(info.Adapters, name)
	}
	// паузить zapret имеет смысл только если VPN реально несёт интернет:
	// маршрут по умолчанию уходит в туннель. Иначе (Tailscale/Clash в режиме
	// mesh) обход всё ещё нужен, а WinDivert не мешает.
	defIdx, ok := defaultRouteIfIndex()
	if !ok {
		return info
	}
	for _, a := range adapters {
		if a.IfIndex == defIdx {
			info.Active = true
			info.Name = a.Name
			if info.Name == "" {
				info.Name = a.Desc
			}
			return info
		}
	}
	return info
}

// defaultRouteIfIndex — индекс интерфейса, через который идёт маршрут по
// умолчанию (IPv4/IPv6, минимальная метрика).
func defaultRouteIfIndex() (uint32, bool) {
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_UNSPEC, &table); err != nil || table == nil {
		return 0, false
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	best := ^uint32(0)
	var idx uint32
	found := false
	for _, row := range table.Rows() {
		if row.DestinationPrefix.PrefixLength != 0 {
			continue
		}
		if row.Loopback != 0 {
			continue
		}
		if row.Metric < best {
			best = row.Metric
			idx = row.InterfaceIndex
			found = true
		}
	}
	return idx, found && idx != 0
}

// tunnelAdapters — список поднятых туннельных адаптеров.
func tunnelAdapters() ([]TunnelAdapter, error) {
	size := uint32(16 * 1024)
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, gaaFlags, 0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			size += 8 * 1024
			continue
		}
		if err != nil {
			return nil, err
		}
		var out []TunnelAdapter
		for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
			if aa.OperStatus != ifOperStatusUp {
				continue
			}
			name := windows.UTF16PtrToString(aa.FriendlyName)
			desc := windows.UTF16PtrToString(aa.Description)
			if !IsTunnelAdapter(aa.IfType, name, desc) {
				continue
			}
			out = append(out, TunnelAdapter{IfIndex: aa.IfIndex, Name: name, Desc: desc, IfType: aa.IfType})
		}
		return out, nil
	}
	return nil, windows.ERROR_BUFFER_OVERFLOW
}

// ClientProcesses — запущенные процессы известных VPN-клиентов (справочно).
func ClientProcesses() []string {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var seen []string
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		name := windows.UTF16ToString(e.ExeFile[:])
		if IsVPNProcess(name) {
			seen = append(seen, strings.ToLower(name))
		}
	}
	return seen
}

// VPN-клиенты, которые поднимают туннель. Idle-клиент сам по себе VPN не
// включает — поэтому этот список не влияет на Active, только на журнал.
var vpnProcesses = []string{
	"openvpn.exe", "openvpnserv.exe", "openvpnserv2.exe", "ovpnagent.exe",
	"wireguard.exe", "wireguard-go.exe",
	"tailscale.exe", "tailscaled.exe",
	"clash.exe", "clash-meta.exe", "clash-verge.exe", "verge-mihomo.exe", "mihomo.exe",
	"v2ray.exe", "xray.exe", "sing-box.exe", "singtun.exe", "tun2socks.exe",
	"hiddify.exe", "hiddify-core.exe", "nekobox.exe", "nekobox_core.exe",
	"amneziawg.exe", "amneziawg-go.exe",
	"nordvpn.exe", "protonvpn.exe", "protonvpn-service.exe", "expressvpn.exe",
	"windscribe.exe", "outline.exe", "shadowsocks.exe", "softether.exe",
}

// IsVPNProcess — процесс из списка известных VPN-клиентов.
func IsVPNProcess(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	for _, p := range vpnProcesses {
		if n == p {
			return true
		}
	}
	return false
}

// части имени/описания адаптера, по которым узнаём туннель
var tunnelNameParts = []string{
	"vpn", "tunnel", "wintun", "wireguard", "tailscale", "tap-windows",
	"openvpn", "clash", "mihomo", "amnezia", "outline", "hiddify",
	"zerotier", "softether", "tap adapter", "tun adapter",
}

// адаптеры, которые похожи на туннели, но VPN не являются
var excludedAdapterParts = []string{
	"teredo", "isatap", "6to4", "loopback", "pseudo", "hyper-v", "vethernet",
	"vmware", "virtualbox", "docker", "wsl", "npcap", "bluetooth", "wi-fi direct",
}

// IsTunnelAdapter — считать ли адаптер VPN-туннелем.
func IsTunnelAdapter(ifType uint32, name, desc string) bool {
	text := strings.ToLower(strings.TrimSpace(name + " " + desc))
	for _, bad := range excludedAdapterParts {
		if strings.Contains(text, bad) {
			return false
		}
	}
	if ifType == ifTypePPP || ifType == ifTypeTunnel {
		return true
	}
	for _, good := range tunnelNameParts {
		if strings.Contains(text, good) {
			return true
		}
	}
	return false
}
