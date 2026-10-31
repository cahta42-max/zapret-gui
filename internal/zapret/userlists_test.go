package zapret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnsureUserLists должен создавать пользовательские списки, на которые
// ссылаются аргументы winws во всех стратегиях (иначе «cannot access ipset file»).
func TestEnsureUserLists(t *testing.T) {
	dir := t.TempDir()
	// структура «дистрибутива»
	os.MkdirAll(filepath.Join(dir, "lists"), 0o755)

	m := New(dir, nil)
	m.EnsureUserLists()

	for name, want := range map[string]string{
		"ipset-exclude-user.txt": "203.0.113.113/32",
		"list-general-user.txt":  "domain.example.abc",
		"list-exclude-user.txt":  "domain.example.abc",
	} {
		data, err := os.ReadFile(filepath.Join(dir, "lists", name))
		if err != nil {
			t.Fatalf("%s не создан: %v", name, err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s: ожидалось вхождение %q, получено %q", name, want, string(data))
		}
	}

	// идемпотентность: повторный вызов не должен портить существующие файлы
	custom := []byte("1.2.3.4/32\r\n")
	os.WriteFile(filepath.Join(dir, "lists", "ipset-exclude-user.txt"), custom, 0o644)
	m.EnsureUserLists()
	got, _ := os.ReadFile(filepath.Join(dir, "lists", "ipset-exclude-user.txt"))
	if string(got) != string(custom) {
		t.Errorf("EnsureUserLists перезаписал пользовательский файл: %q", string(got))
	}
}

// WriteUserList: валидация строк, заглушка вместо пустого списка, CRLF-независимость.
func TestWriteUserList(t *testing.T) {
	m := New(t.TempDir(), nil)

	// нормальная запись доменов (с комментарием и пустой строкой)
	if err := m.WriteUserList("general", "# мой комментарий\n\njohnsad.ventures\r\n^dns.google"); err != nil {
		t.Fatalf("general: %v", err)
	}
	got, _ := m.ReadUserList("general")
	for _, want := range []string{"# мой комментарий", "johnsad.ventures", "^dns.google"} {
		if !strings.Contains(got, want) {
			t.Errorf("general: нет строки %q в %q", want, got)
		}
	}

	// IP/CIDR
	if err := m.WriteUserList("ipset-exclude", "203.0.113.113/32\n8.8.8.8"); err != nil {
		t.Fatalf("ipset-exclude: %v", err)
	}

	// мусор в домене — ошибка с номером строки
	if err := m.WriteUserList("exclude", "ok.example\nсайт с пробелами"); err == nil {
		t.Error("ожидалась ошибка валидации домена")
	} else if !strings.Contains(err.Error(), "строка 2") {
		t.Errorf("ошибка без номера строки: %v", err)
	}

	// мусорный CIDR
	if err := m.WriteUserList("ipset-exclude", "10.0.0.999/24"); err == nil {
		t.Error("ожидалась ошибка валидации CIDR")
	}

	// пустой список → заглушка (winws не должен остаться с пустым hostlist)
	if err := m.WriteUserList("exclude", "\n# только комментарий\n"); err != nil {
		t.Fatalf("пустой exclude: %v", err)
	}
	got, _ = m.ReadUserList("exclude")
	if strings.Trim(got, "\r\n ") == "" {
		t.Error("пустой список записан без заглушки")
	}

	// неизвестный kind
	if err := m.WriteUserList("unknown", "x"); err == nil {
		t.Error("ожидалась ошибка про неизвестный список")
	}
}

// GetUserDomains/SetUserDomains: заглушки дистрибутива скрыты от GUI,
// полный сброс не ломает winws (пишется заглушка).
func TestGetSetUserDomains(t *testing.T) {
	m := New(t.TempDir(), nil)

	// файл с миксом: заглушка, комментарий, живые домены
	p, _ := m.UserListPath("general")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("# Never leave this file empty\r\ndomain.example.abc\njohnsad.ventures\n^dns.google\n"), 0o644)

	got, err := m.GetUserDomains()
	if err != nil {
		t.Fatalf("GetUserDomains: %v", err)
	}
	if len(got["general"]) != 2 || got["general"][0] != "johnsad.ventures" || got["general"][1] != "^dns.google" {
		t.Errorf("general: ожидались только живые домены, получено %v", got["general"])
	}
	if len(got["exclude"]) != 0 || len(got["ipset-exclude"]) != 0 {
		t.Errorf("пустые списки должны читаться пустыми: %v", got)
	}

	// SetUserDomains: запись и повторное чтение без потерь
	if err := m.SetUserDomains(map[string][]string{
		"exclude": {"zendesk.com", "cloudfront.net"},
	}); err != nil {
		t.Fatalf("SetUserDomains: %v", err)
	}
	got, _ = m.GetUserDomains()
	if len(got["exclude"]) != 2 {
		t.Errorf("exclude после записи: %v", got["exclude"])
	}

	// полный сброс — не ошибка, файл остаётся рабочим (заглушка)
	if err := m.SetUserDomains(map[string][]string{"exclude": {}}); err != nil {
		t.Fatalf("сброс exclude: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(filepath.Dir(p), "list-exclude-user.txt"))
	if len(strings.TrimSpace(string(data))) == 0 {
		t.Error("после сброса файл пуст без заглушки — winws может упасть")
	}

	// мусор отдаётся с kind в ошибке
	if err := m.SetUserDomains(map[string][]string{"general": {"плохой домен"}}); err == nil {
		t.Error("ожидалась ошибка валидации")
	} else if !strings.Contains(err.Error(), "general") {
		t.Errorf("ошибка без kind: %v", err)
	}
}
