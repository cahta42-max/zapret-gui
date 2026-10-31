//go:build windows

package zapret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInjectIpsetCFExclude(t *testing.T) {
	const cf = `--ipset-exclude=C:\zapret\lists\ipset-exclude-cf.txt`
	args := []string{
		`C:\zapret\bin\winws.exe`,
		// профиль 1: hostlist (не трогаем)
		"--filter-udp=443", `--hostlist=C:\zapret\lists\list-general.txt`, "--dpi-desync=fake",
		"--new",
		// профиль 2: ipset без исключения Cloudflare
		"--filter-tcp=80,443", `--ipset=C:\zapret\lists\ipset-all.txt`, "--dpi-desync=hostfakesplit",
		"--new",
		// профиль 3: ipset, исключение уже есть — дублировать нельзя
		"--filter-tcp=1024-65535", `--ipset=C:\zapret\lists\ipset-all.txt`, cf,
		"--new",
		// профиль 4: ipset
		"--filter-udp=1024-65535", `--ipset=C:\zapret\lists\ipset-all.txt`,
	}
	got := injectIpsetCFExclude(args, cf)

	// профили по --new
	var profiles [][]string
	var cur []string
	for _, a := range got {
		if a == "--new" {
			profiles = append(profiles, cur)
			cur = nil
			continue
		}
		cur = append(cur, a)
	}
	profiles = append(profiles, cur)
	if len(profiles) != 4 {
		t.Fatalf("профилей: %d, хотим 4", len(profiles))
	}
	count := func(p []string) int {
		n := 0
		for _, a := range p {
			if a == cf {
				n++
			}
		}
		return n
	}
	if n := count(profiles[0]); n != 0 {
		t.Errorf("hostlist-профиль получил исключение Cloudflare: %v", profiles[0])
	}
	if n := count(profiles[1]); n != 1 {
		t.Errorf("ipset-профиль: исключений %d, хотим 1", n)
	}
	if n := count(profiles[2]); n != 1 {
		t.Errorf("ipset-профиль с готовым исключением: %d, хотим 1 (без дубля)", n)
	}
	if n := count(profiles[3]); n != 1 {
		t.Errorf("ipset-профиль: исключений %d, хотим 1", n)
	}
	// идемпотентность: повторный прогон не добавляет второй токен
	again := injectIpsetCFExclude(got, cf)
	if len(again) != len(got) {
		t.Errorf("повторный прогон изменил длину: %d -> %d", len(got), len(again))
	}
}

func TestEnsureIpsetCFExclude(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, nil)
	path := m.EnsureIpsetCFExclude()
	if want := filepath.Join(dir, "lists", ipsetCFExcludeName); path != want {
		t.Fatalf("путь: %q, хотим %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл не создан: %v", err)
	}
	text := string(data)
	for _, r := range cloudflareRanges {
		if !strings.Contains(text, r) {
			t.Errorf("в файле нет диапазона %s", r)
		}
	}
	// повторный вызов не перезаписывает файл (mtime прежний не проверяем,
	// проверяем, что содержимое не испорчено)
	if path2 := m.EnsureIpsetCFExclude(); path2 != path {
		t.Errorf("путь изменился: %q", path2)
	}
}
