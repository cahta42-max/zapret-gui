// exver — dev-утилита: проверка чтения версии из exe-ресурсов.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"zapret_gui/internal/winutil"
)

func main() {
	exe := filepath.Join(os.Getenv("LOCALAPPDATA"), "ZapretGui", "tools", "tg-ws-proxy", "TgWsProxy.exe")
	fmt.Printf("%s\n  -> %q\n", exe, winutil.ExeFileVersion(exe))
	fmt.Printf("себя -> %q\n", winutil.ExeFileVersion(os.Args[0]))
}
