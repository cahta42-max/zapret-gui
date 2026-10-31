//go:build darwin && !cgo

// ЗАГЛУШКА для типовой проверки darwin-сборки без cgo (см. tools/darcheck):
// внутренний cgo-фронтенд wails исключается из сборки, этот файл подменяет
// фабрику NewFrontend, чтобы остальное дерево типизировалось. На реальном
// Mac НЕ используется.
package desktop

import (
	"context"

	"github.com/wailsapp/wails/v2/internal/binding"
	"github.com/wailsapp/wails/v2/internal/frontend"
	"github.com/wailsapp/wails/v2/internal/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
)

func NewFrontend(ctx context.Context, appoptions *options.App, logger *logger.Logger, appBindings *binding.Bindings, dispatcher frontend.Dispatcher) frontend.Frontend {
	return nil
}
