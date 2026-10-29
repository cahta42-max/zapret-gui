//go:build windows

package zapret

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"zapret_gui/internal/winutil"
)

// TermResult — итог терминального теста по одной стратегии (DPI-чекеры).
type TermResult struct {
	Name    string `json:"name"`
	Ok      int    `json:"ok"`
	Fail    int    `json:"fail"`
	Unsup   int    `json:"unsup"`
	Blocked int    `json:"blocked"`
}

// RunTerminalTest запускает ОРИГИНАЛЬНЫЙ тестовый скрипт дистрибутива
// (utils/test zapret.ps1; режим DPI-чекеров TCP 16-20, все конфиги) —
// тот же прогон, что и через терминал, с теми же внешними целями
// (36 хостов, HTTP/TLS1.2/TLS1.3) и той же аналитикой. Возвращает итоги
// по стратегиям и лучшую стратегию из файла результатов скрипта.
//
// ТРЕБОВАНИЯ скрипта: права администратора (GUI уже elevated) и ОТСУТСТВИЕ
// службы zapret — вызывающий код должен снять службу до запуска.
// customHost (необязательно) замещает набор целей одним хостом
// (env MONITOR_HOST, как в скрипте).
func (m *Manager) RunTerminalTest(customHost string,
	onLine func(string), onProgress func(done, total int, strategy string)) ([]TermResult, string, error) {

	script := filepath.Join(m.dir, "utils", "test zapret.ps1")
	if _, err := os.Stat(script); err != nil {
		return nil, "", fmt.Errorf("не найден test zapret.ps1: %w", err)
	}
	resultsDir := filepath.Join(m.dir, "utils", "test results")
	started := time.Now()

	// stdin: «2» — DPI-чекеры, «1» — все конфиги (интерактивные вопросы скрипта)
	cmd := winutil.HiddenCmd("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", script)
	cmd.Dir = m.dir
	cmd.Stdin = strings.NewReader("2\r\n1\r\n")
	if h := strings.TrimSpace(customHost); h != "" {
		cmd.Env = append(os.Environ(), "MONITOR_HOST="+h)
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}

	// ход прогона: строки «[N/M] general (ALT).bat» + весь вывод в журнал
	progRe := regexp.MustCompile(`\[\s*(\d+)/(\d+)\]\s*(.+?)\.bat`)
	scan := func(r io.Reader) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 256*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if mm := progRe.FindStringSubmatch(line); mm != nil && onProgress != nil {
				d, _ := strconv.Atoi(mm[1])
				t, _ := strconv.Atoi(mm[2])
				onProgress(d, t, mm[3])
			}
			if onLine != nil {
				onLine(line)
			}
		}
	}
	go scan(stdout)
	go scan(stderr)
	_ = cmd.Wait() // итог — в файле результатов; долгий прогон, ошибку stdin не считаем фатальной

	// самый свежий test_results_*.txt, созданный ПОСЛЕ старта прогона
	newest, found := "", false
	var mt time.Time
	if entries, err := os.ReadDir(resultsDir); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), "test_results_") {
				continue
			}
			info, err := e.Info()
			if err != nil || !info.ModTime().After(started.Add(-2*time.Second)) {
				continue
			}
			if !found || info.ModTime().After(mt) {
				found, mt, newest = true, info.ModTime(), filepath.Join(resultsDir, e.Name())
			}
		}
	}
	if !found {
		return nil, "", errors.New("тестовый скрипт не создал файл результатов")
	}
	return ParseTermResults(newest)
}

var (
	termLineRe = regexp.MustCompile(`^(\S.*?)\s*:\s*OK:\s*(\d+),\s*FAIL:\s*(\d+),\s*UNSUP:\s*(\d+),\s*BLOCKED:\s*(\d+)\s*$`)
	termBestRe = regexp.MustCompile(`^Best strategy:\s*(.+?)\s*$`)
)

// ParseTermResults разбирает итоговый файл test_results_*.txt
// (секция «=== ANALYTICS ===» и строка «Best strategy: …»).
func ParseTermResults(path string) ([]TermResult, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var results []TermResult
	best := ""
	for _, line := range strings.Split(string(data), "\r\n") {
		line = strings.TrimRight(line, "\r")
		if m := termLineRe.FindStringSubmatch(line); m != nil {
			r := TermResult{Name: strings.TrimSpace(m[1])}
			r.Ok, _ = strconv.Atoi(m[2])
			r.Fail, _ = strconv.Atoi(m[3])
			r.Unsup, _ = strconv.Atoi(m[4])
			r.Blocked, _ = strconv.Atoi(m[5])
			results = append(results, r)
			continue
		}
		if m := termBestRe.FindStringSubmatch(line); m != nil {
			best = strings.TrimSuffix(strings.TrimSpace(m[1]), ".bat")
		}
	}
	if len(results) == 0 {
		return nil, "", errors.New("в файле результатов нет аналитики")
	}
	return results, best, nil
}

// HostOf возвращает хост из URL ("https://a.b/c" → "a.b") — для MONITOR_HOST.
func HostOf(rawURL string) string {
	s := strings.TrimSpace(rawURL)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}
