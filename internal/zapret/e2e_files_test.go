package zapret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAllReferencedFilesExist — E2E-проверка установленного дистрибутива:
// для каждой стратегии разворачиваем аргументы и убеждаемся, что каждый
// файл (.bin/.txt), на который ссылаются файловые флаги, существует.
// Инлайновые значения (0x..., «!», домены discord.media) файлами не являются.
func TestAllReferencedFilesExist(t *testing.T) {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "ZapretGui", "tools", "zapret")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("дистрибутив не установлен: %s", dir)
	}

	m := New(dir, nil)
	m.EnsureUserLists()
	strategies, err := ScanStrategies(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(strategies) == 0 {
		t.Fatal("стратегии не найдены")
	}

	// флаги, значение которых может быть путём к файлу
	fileFlags := map[string]bool{
		"--hostlist": true, "--hostlist-exclude": true,
		"--ipset": true, "--ipset-exclude": true,
		"--dpi-desync-fake-quic": true, "--dpi-desync-fake-discord": true,
		"--dpi-desync-fake-stun": true, "--dpi-desync-fake-unknown-udp": true,
		"--dpi-desync-fake-tls": true, "--dpi-desync-fake-http": true,
		"--dpi-desync-split-seqovl-pattern": true, "--dpi-desync-fake-syndata": true,
	}

	checkPath := func(name, mode, path string) {
		if !strings.HasSuffix(path, ".bin") && !strings.HasSuffix(path, ".txt") {
			return // инлайновое значение (0x00000000, !, домены)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("[%s/%s] %s: %v", name, mode, path, err)
		}
	}

	// проверяем во всех режимах game filter
	for _, mode := range []string{"off", "all"} {
		tcp, udp := GameFilterPorts(mode)
		vars := Vars{
			BinDir:        filepath.Join(dir, "bin") + string(os.PathSeparator),
			ListsDir:      filepath.Join(dir, "lists") + string(os.PathSeparator),
			RootDir:       dir + string(os.PathSeparator),
			GameFilterTCP: tcp,
			GameFilterUDP: udp,
		}
		for _, s := range strategies {
			args := ExpandArgs(s.RawArgs, vars)
			expectFile := false
			for _, a := range args {
				if strings.HasPrefix(a, "--") {
					if i := strings.IndexByte(a, '='); i >= 0 {
						if fileFlags[a[:i]] {
							checkPath(s.Name, mode, a[i+1:])
						}
						continue
					}
					expectFile = fileFlags[a]
					continue
				}
				if expectFile {
					checkPath(s.Name, mode, a)
					expectFile = false
				}
			}
		}
	}
	t.Logf("проверено %d стратегий × 2 режима game filter — все файлы на месте", len(strategies))
}
