package zapret

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Strategy — одна стратегия из general*.bat.
type Strategy struct {
	Name    string   `json:"name"` // имя файла без .bat, например "general (ALT2)"
	File    string   `json:"file"` // полный путь к bat-файлу
	RawArgs []string `json:"-"`    // аргументы winws.exe с %-переменными
}

// Vars — значения переменных, подставляемых в аргументы при запуске.
type Vars struct {
	BinDir        string // <zapret>\bin\
	ListsDir      string // <zapret>\lists\
	RootDir       string // <zapret>\
	GameFilterTCP string // "12" (выкл) или "1024-65535"
	GameFilterUDP string
}

// ScanStrategies находит все стратегии (general*.bat, кроме service*).
func ScanStrategies(zapretDir string) ([]Strategy, error) {
	entries, err := os.ReadDir(zapretDir)
	if err != nil {
		return nil, err
	}
	var list []Strategy
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".bat") {
			continue
		}
		if strings.HasPrefix(strings.ToLower(name), "service") {
			continue
		}
		s, err := ParseStrategy(filepath.Join(zapretDir, name))
		if err != nil {
			continue // некорректный bat — пропускаем
		}
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool { return naturalLess(list[i].Name, list[j].Name) })
	return list, nil
}

// naturalLess сортирует "ALT2" перед "ALT10" (как в service.bat).
func naturalLess(a, b string) bool {
	ai, bi := 0, 0
	for ai < len(a) && bi < len(b) {
		ca, cb := a[ai], b[bi]
		if isDigit(ca) && isDigit(cb) {
			// читаем числа целиком
			aj, bj := ai, bi
			for aj < len(a) && isDigit(a[aj]) {
				aj++
			}
			for bj < len(b) && isDigit(b[bj]) {
				bj++
			}
			na, nb := strings.TrimLeft(a[ai:aj], "0"), strings.TrimLeft(b[bi:bj], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			ai, bi = aj, bj
			continue
		}
		if ca != cb {
			return ca < cb
		}
		ai++
		bi++
	}
	return len(a)-ai < len(b)-bi
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// ParseStrategy извлекает из bat-файла аргументы запуска winws.exe.
func ParseStrategy(batPath string) (*Strategy, error) {
	data, err := os.ReadFile(batPath)
	if err != nil {
		return nil, err
	}
	// bat использует chcp 65001 → UTF-8
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")

	// Склеиваем продолжения строк (строка заканчивается на ^)
	var logical []string
	for _, raw := range lines {
		trimmed := strings.TrimRight(raw, " \t")
		if len(logical) > 0 {
			prev := logical[len(logical)-1]
			if strings.HasSuffix(prev, "^") && !strings.HasSuffix(prev, "^^") {
				logical[len(logical)-1] = strings.TrimSuffix(prev, "^") + trimmed
				continue
			}
		}
		logical = append(logical, trimmed)
	}

	// Ищем строку запуска winws.exe
	for _, line := range logical {
		idx := strings.Index(line, "winws.exe")
		if idx < 0 {
			continue
		}
		// отсекаем префикс `start "..." /min "...winws.exe" `
		rest := line[idx+len("winws.exe"):]
		rest = strings.TrimPrefix(rest, `"`)
		args, err := tokenize(rest)
		if err != nil {
			return nil, err
		}
		// отрезаем хвостовой мусор (например, команды после запуска)
		return &Strategy{
			Name:    strings.TrimSuffix(filepath.Base(batPath), filepath.Ext(batPath)),
			File:    batPath,
			RawArgs: args,
		}, nil
	}
	return nil, fmt.Errorf("в %s не найден запуск winws.exe", filepath.Base(batPath))
}

// tokenize разбивает командную строку на токены с учётом кавычек.
func tokenize(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case (c == ' ' || c == '\t') && !inQuote:
			flush()
		case c == '^' && !inQuote:
			// экранирующий символ cmd — пропускаем
			continue
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return tokens, nil
}

// ExpandArgs подставляет переменные окружения bat-файла в токены.
func ExpandArgs(raw []string, v Vars) []string {
	out := make([]string, 0, len(raw))
	rep := strings.NewReplacer(
		"%BIN%", v.BinDir,
		"%LISTS%", v.ListsDir,
		"%~dp0", v.RootDir,
		"%GameFilterTCP%", v.GameFilterTCP,
		"%GameFilterUDP%", v.GameFilterUDP,
	)
	for _, t := range raw {
		t = rep.Replace(t)
		// раскрываем %~n0 (имя bat) — в аргументах не встречается, но на всякий случай
		if strings.Contains(t, "%~n0") {
			t = strings.ReplaceAll(t, "%~n0", "zapret")
		}
		out = append(out, t)
	}
	return out
}
