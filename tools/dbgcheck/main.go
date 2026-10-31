// dbgcheck — dev-утилита: проверяет работу isProcessRunning/tasklist
// точно так же, как это делает GUI в рантайме.
package main

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

func main() {
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq TgWsProxy.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	out, err := cmd.Output()
	fmt.Printf("err=%v len=%d\n", err, len(out))
	fmt.Printf("contains=%v\n", strings.Contains(strings.ToLower(string(out)), "tgwsproxy.exe"))
	fmt.Println("--- сырой вывод ---")
	fmt.Println(string(out))
}
