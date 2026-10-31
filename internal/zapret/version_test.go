package zapret

import (
	"os"
	"path/filepath"
	"testing"
)

// Релизный zip Flowseal не содержит .service/version.txt — версия берётся
// из LOCAL_VERSION в service.bat.
func TestVersionFromServiceBat(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, nil)

	if v := m.Version(); v != "" {
		t.Fatalf("пустой дистрибутив: ожидалось \"\", получено %q", v)
	}

	bat := "@echo off\r\nset \"LOCAL_VERSION=1.9.7\"\r\n\r\n:: MENU\r\n"
	os.WriteFile(filepath.Join(dir, "service.bat"), []byte(bat), 0o644)
	if v := m.Version(); v != "1.9.7" {
		t.Fatalf("ожидалось 1.9.7, получено %q", v)
	}

	// version.txt имеет приоритет
	os.MkdirAll(filepath.Join(dir, ".service"), 0o755)
	os.WriteFile(filepath.Join(dir, ".service", "version.txt"), []byte("2.0.0\r\n"), 0o644)
	if v := m.Version(); v != "2.0.0" {
		t.Fatalf("ожидался приоритет version.txt: получено %q", v)
	}
}
