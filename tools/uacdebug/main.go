// Отладка элевации через задачу планировщика: повторяет шаги
// winutil.TryLaunchElevatedViaTask с подробным выводом.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func main() {
	fmt.Println("admin:", isAdmin())
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("os.Executable:", err)
		return
	}
	// в go run os.Executable() — временный бинарник; для отладки путь
	// настоящий exe передаём аргументом
	if len(os.Args) > 1 {
		exe = os.Args[1]
	}
	fmt.Println("exe:", exe)

	cmd := exec.Command("schtasks", "/Query", "/TN", "ZapretGui", "/XML")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	xml, err := cmd.Output()
	fmt.Printf("query err=%v len=%d\n", err, len(xml))
	if len(xml) > 0 {
		fmt.Printf("first bytes: % x\n", xml[:min(16, len(xml))])
		fmt.Println("contains raw:", strings.Contains(string(xml), exe))
		var sb strings.Builder
		for i := 0; i+1 < len(xml); i += 2 {
			sb.WriteByte(xml[i])
		}
		fmt.Println("contains utf16le:", strings.Contains(sb.String(), exe))
		// показать фрагмент <Command>
		if i := strings.Index(sb.String(), "<Command>"); i >= 0 {
			fmt.Println("command tag:", sb.String()[i:i+min(160, len(sb.String())-i)])
		}
		if i := strings.Index(string(xml), "<Command>"); i >= 0 {
			fmt.Println("command raw:", string(xml)[i:i+min(160, len(string(xml))-i)])
		}
	}

	fmt.Println("run via task? введите y для запуска:")
	var ans string
	fmt.Scanln(&ans)
	if ans == "y" {
		out, err := exec.Command("schtasks", "/Run", "/TN", "ZapretGui").CombinedOutput()
		fmt.Printf("run err=%v out=%q\n", err, strings.TrimSpace(string(out)))
	}
}

func isAdmin() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

func min(a, b int) int { if a < b { return a }; return b }
