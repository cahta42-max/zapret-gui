// argcheck — dev-утилита: печатает точную команду winws.exe для стратегии,
// которую сгенерировал бы GUI (с учётом текущих флагов game filter / ipset),
// и готовит скрипт E2E-проверки запуска.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zapret_gui/internal/zapret"
)

func main() {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "ZapretGui", "tools", "zapret")
	m := zapret.New(dir, nil)
	m.EnsureUserLists()

	strategy := "general (ALT9)"
	if len(os.Args) > 1 {
		strategy = os.Args[1]
	}
	argv, err := m.CommandArgs(strategy)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
	cmdline := strings.Join(argv, " ")

	tmp := os.TempDir()
	script := fmt.Sprintf(`$p = Start-Process -FilePath '%s' -ArgumentList '%s' -RedirectStandardError '%s\winws_e2e.err' -RedirectStandardOutput '%s\windiv_e2e.out' -PassThru -WindowStyle Hidden
Start-Sleep -Seconds 6
if ($p.HasExited) {
  ('EXITED code=' + $p.ExitCode) | Out-File '%s\winws_e2e.res' -Encoding utf8
} else {
  'RUNNING_OK' | Out-File '%s\winws_e2e.res' -Encoding utf8
  Stop-Process -Id $p.Id -Force
}`, argv[0], strings.Join(argv[1:], " "), tmp, tmp, tmp, tmp)
	_ = os.WriteFile(filepath.Join(tmp, "winws_e2e.ps1"), []byte(script), 0o644)

	fmt.Println("STRATEGY:", strategy)
	fmt.Println("GAME MODE:", m.GameFilterMode(), "| IPSET:", m.IpsetStatus())
	fmt.Println("CMD:", cmdline)
	fmt.Println("PS1:", filepath.Join(tmp, "winws_e2e.ps1"))
}
