package zapret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Тесты парсера стратегий на реальных general*.bat из репозитория Flowseal.
func TestParseRealBats(t *testing.T) {
	files, err := filepath.Glob("testdata/general*.bat")
	if err != nil || len(files) == 0 {
		t.Skipf("нет фикстур: %v", err)
	}
	for _, f := range files {
		s, err := ParseStrategy(f)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if len(s.RawArgs) < 5 {
			t.Errorf("%s: подозрительно мало аргументов (%d)", s.Name, len(s.RawArgs))
		}
		joined := strings.Join(s.RawArgs, " ")
		if !strings.Contains(joined, "--wf-tcp=") {
			t.Errorf("%s: нет --wf-tcp в аргументах: %s", s.Name, joined)
		}
		t.Logf("%s → %d токенов", s.Name, len(s.RawArgs))
	}
}

func TestExpandArgs(t *testing.T) {
	s, err := ParseStrategy("testdata/general.bat")
	if err != nil {
		t.Fatal(err)
	}
	vars := Vars{
		BinDir:        `C:\tools\zapret\bin\`,
		ListsDir:      `C:\tools\zapret\lists\`,
		RootDir:       `C:\tools\zapret\`,
		GameFilterTCP: "12",
		GameFilterUDP: "12",
	}
	expanded := ExpandArgs(s.RawArgs, vars)
	joined := strings.Join(expanded, " ")

	for _, want := range []string{
		`--hostlist=C:\tools\zapret\lists\list-general.txt`,
		`--dpi-desync-fake-quic=C:\tools\zapret\bin\quic_initial_www_google_com.bin`,
		"--wf-tcp=80,443,2053,2083,2087,2096,8443,12",
		"--wf-udp=443,19294-19344,50000-50100,12",
		"--new",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("в развернутых аргументах нет %q\n%s", want, joined)
		}
	}
	// не осталось переменных
	if strings.Contains(joined, "%BIN%") || strings.Contains(joined, "%LISTS%") ||
		strings.Contains(joined, "%~dp0") {
		t.Errorf("неразвернутые переменные: %s", joined)
	}
}

func TestScanAndNaturalSort(t *testing.T) {
	// каталог с фикстурами — как будто это дистрибутив
	dir := t.TempDir()
	files, _ := filepath.Glob("testdata/general*.bat")
	for _, f := range files {
		data, _ := os.ReadFile(f)
		os.WriteFile(filepath.Join(dir, filepath.Base(f)), data, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "service.bat"), []byte("@echo off"), 0o644)

	list, err := ScanStrategies(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != len(files) {
		t.Fatalf("ожидалось %d стратегий, получено %d", len(files), len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Name == list[i].Name {
			t.Error("дубликаты в списке стратегий")
		}
	}
	t.Logf("порядок: %v", names(list))
}

func names(list []Strategy) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s.Name
	}
	return out
}
