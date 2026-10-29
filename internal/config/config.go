// Package config хранит настройки Zapret_gui.
// Данные кладём в %LOCALAPPDATA%\ZapretGui, либо в портативную папку
// Zapret_gui_data рядом с exe (если она существует).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

const AppName = "ZapretGui"

type Config struct {
	ToolsDir       string `json:"toolsDir"`       // где живут скачанные сервисы
	LastStrategy   string `json:"lastStrategy"`   // последняя выбранная стратегия zapret
	StrategyChosen bool   `json:"strategyChosen"` // юзер хоть раз явно выбирал/запускал стратегию
	ZapretAutoRun  bool   `json:"zapretAutoRun"`  // авто-подъём zapret при старте GUI (после первого выбора)
	AutostartGui   bool   `json:"autostartGui"`   // автозапуск GUI при входе (задача планировщика)
	StartMinimized bool   `json:"startMinimized"` // стартовать свёрнутым в трей
	ZapretVersion  string `json:"zapretVersion"`  // версия скачанного zapret
	TgwsVersion    string `json:"tgwsVersion"`    // версия скачанного tg-ws-proxy
}

// DataDir возвращает каталог данных приложения.
func DataDir() string {
	// Портативный режим: папка Zapret_gui_data рядом с exe
	if exe, err := os.Executable(); err == nil {
		portable := filepath.Join(filepath.Dir(exe), "Zapret_gui_data")
		if st, err := os.Stat(portable); err == nil && st.IsDir() {
			return portable
		}
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", AppName)
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		home, _ := os.UserHomeDir()
		local = home
	}
	return filepath.Join(local, AppName)
}

// DefaultToolsDir — стандартное расположение сервисов.
func DefaultToolsDir() string {
	return filepath.Join(DataDir(), "tools")
}

func Path() string {
	return filepath.Join(DataDir(), "config.json")
}

// Load читает конфиг или создаёт с значениями по умолчанию.
func Load() (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(Path())
	if err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			cfg = &Config{} // повреждённый конфиг — начинаем с чистого
		}
		// ZapretAutoRun появился не сразу: в старых конфигах ключа нет —
		// включаем (zapret должен подниматься сам, без ручного запуска)
		if !jsonHasKey(data, "zapretAutoRun") {
			cfg.ZapretAutoRun = true
		}
		// StrategyChosen появился позже (qa-2 №2): авто-подъём включается
		// только после первого явного выбора стратегии. Старый конфиг с
		// сохранённой стратегией считаем «выбранной» — поведение не меняем.
		if !jsonHasKey(data, "strategyChosen") {
			cfg.StrategyChosen = cfg.LastStrategy != ""
		}
	} else {
		cfg.ZapretAutoRun = true // новый конфиг — авто-подъём zapret включён
		// StrategyChosen=false: до первого выбора стратегии ничего не запускаем
	}
	if cfg.ToolsDir == "" {
		cfg.ToolsDir = DefaultToolsDir()
	}
	return cfg, nil
}

// jsonHasKey — есть ли ключ верхнего уровня в JSON-конфиге
// (чтобы отличить «false» от «ключа нет»).
func jsonHasKey(data []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// Save записывает конфиг на диск.
func (c *Config) Save() error {
	if err := os.MkdirAll(DataDir(), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), data, 0o644)
}
