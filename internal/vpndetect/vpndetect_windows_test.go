//go:build windows

package vpndetect

import "testing"

func TestIsVPNProcess(t *testing.T) {
	yes := []string{"wireguard.exe", "WireGuard.EXE", "openvpn.exe", "tailscaled.exe",
		"mihomo.exe", "clash-verge.exe", "amneziawg.exe", "sing-box.exe"}
	for _, n := range yes {
		if !IsVPNProcess(n) {
			t.Errorf("%s должен считаться VPN-клиентом", n)
		}
	}
	no := []string{"chrome.exe", "winws.exe", "notepad.exe", "wireguard", "vpn.exe", ""}
	for _, n := range no {
		if IsVPNProcess(n) {
			t.Errorf("%s не должен считаться VPN-клиентом", n)
		}
	}
}

func TestIsTunnelAdapter(t *testing.T) {
	cases := []struct {
		name   string
		ifType uint32
		ifName string
		desc   string
		want   bool
	}{
		{"PPP/L2TP", ifTypePPP, "VPN-подключение", "WAN Miniport (L2TP)", true},
		{"TUNNEL по типу", ifTypeTunnel, "Ethernet 5", "Some tunnel", true},
		{"WireGuard по имени", 6, "WireGuard Tunnel", "WireGuard", true},
		{"TAP-Windows по описанию", 6, "Ethernet 2", "TAP-Windows Adapter V9", true},
		{"Tailscale", 6, "Tailscale", "Tailscale Tunnel", true},
		{"Clash/Wintun", 6, "Clash", "Wintun Userspace Tunnel", true},
		{"обычный Ethernet", 6, "Ethernet", "Intel(R) Ethernet Connection", false},
		{"Wi-Fi", 71, "Wi-Fi", "Intel(R) Wi-Fi 6 AX201", false},
		{"Teredo исключён", ifTypeTunnel, "Teredo Tunneling Pseudo-Interface", "", false},
		{"ISATAP исключён", ifTypeTunnel, "isatap.example.com", "", false},
		{"Hyper-V не VPN", 6, "vEthernet (Default Switch)", "Hyper-V Virtual Ethernet Adapter", false},
		{"WSL не VPN", 6, "vEthernet (WSL)", "Hyper-V Virtual Ethernet Adapter", false},
		{"Docker не VPN", 6, "vEthernet (docker0)", "Docker bridge", false},
	}
	for _, c := range cases {
		if got := IsTunnelAdapter(c.ifType, c.ifName, c.desc); got != c.want {
			t.Errorf("%s: got=%v want=%v (ifType=%d, name=%q, desc=%q)",
				c.name, got, c.want, c.ifType, c.ifName, c.desc)
		}
	}
}
