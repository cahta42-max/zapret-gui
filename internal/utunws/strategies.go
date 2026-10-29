//go:build darwin

package utunws

import (
	"os"
	"path/filepath"
	"strings"
)

// Strategy — стратегия движка: id (имя файла .conf.in, пишется в
// selected-strategy) и заголовок (как на Windows: «general (ALT9)»).
type Strategy struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// builtinStrategies — fallback, если tsv недоступен (до первой загрузки).
var builtinStrategies = []Strategy{
	{"general", "general"},
	{"general-alt", "general (ALT)"},
	{"general-alt2", "general (ALT2)"},
	{"general-alt3", "general (ALT3)"},
	{"general-alt4", "general (ALT4)"},
	{"general-alt5", "general (ALT5)"},
	{"general-alt6", "general (ALT6)"},
	{"general-alt7", "general (ALT7)"},
	{"general-alt8", "general (ALT8)"},
	{"general-alt9", "general (ALT9)"},
	{"general-alt10", "general (ALT10)"},
	{"general-alt11", "general (ALT11)"},
	{"general-alt12", "general (ALT12)"},
	{"general-exp", "general (EXP)"},
	{"general-fake-tls-auto", "general (FAKE TLS AUTO)"},
	{"general-fake-tls-auto-alt", "general (FAKE TLS AUTO ALT)"},
	{"general-fake-tls-auto-alt2", "general (FAKE TLS AUTO ALT2)"},
	{"general-fake-tls-auto-alt3", "general (FAKE TLS AUTO ALT3)"},
	{"general-simple-fake", "general (SIMPLE FAKE)"},
	{"general-simple-fake-alt", "general (SIMPLE FAKE ALT)"},
	{"general-simple-fake-alt2", "general (SIMPLE FAKE ALT2)"},
}

// Strategies — список стратегий из strategies.tsv дистрибутива.
func (m *Manager) Strategies() []Strategy {
	data, err := os.ReadFile(filepath.Join(m.PayloadSrc(), "strategies.tsv"))
	if err != nil {
		return builtinStrategies
	}
	var list []Strategy
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 2 || fields[0] == "" {
			continue
		}
		id, title := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		// валидный id: general, general-alt9, general-fake-tls-auto-alt3…
		ok := id == "general" || strings.HasPrefix(id, "general-")
		if ok {
			list = append(list, Strategy{ID: id, Title: title})
		}
	}
	if len(list) == 0 {
		return builtinStrategies
	}
	return list
}

// StrategyByID / StrategyByTitle — поиск в текущем списке стратегий.
func (m *Manager) StrategyByID(id string) (Strategy, bool) {
	for _, s := range m.Strategies() {
		if s.ID == id {
			return s, true
		}
	}
	return Strategy{}, false
}

func (m *Manager) StrategyByTitle(title string) (Strategy, bool) {
	for _, s := range m.Strategies() {
		if s.Title == title {
			return s, true
		}
	}
	// на случай сохранённого в конфиге id вместо названия
	for _, s := range m.Strategies() {
		if s.ID == title {
			return s, true
		}
	}
	return Strategy{}, false
}

// DefaultStrategyID — стратегия по умолчанию.
const DefaultStrategyID = "general"
