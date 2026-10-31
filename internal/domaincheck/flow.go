package domaincheck

import (
	"errors"
	"fmt"

	"zapret_gui/internal/strattest"
)

// Flow — сценарий автопроверки домена: проверить «с обходом», остановить
// обход, проверить «без», выбрать режим, применить его и безусловно
// перезапустить обход. Всё внешнее — колбэки, поэтому порядок шагов,
// ошибки и решения тестируются без GUI и без живой сети.
//
// Важное свойство: если обход уже остановлен, он ОБЯЗАТЕЛЬНО поднимается
// обратно на любом пути — даже при ошибке. Иначе пользователь остался бы
// без обхода.
type Flow struct {
	// Test — проверка одного URL при текущем состоянии обхода.
	Test func(url string) strattest.UrlTestResult
	// Stop — остановить обход (службу и/или процесс).
	Stop func() error
	// Restart — поднять обход обратно с последней стратегией.
	Restart func() error
	// Apply — записать домен в список kind (добавлен ли, из какого списка
	// перенесён, ошибка).
	Apply func(kind, domain string) (added bool, movedFrom string, err error)
	// Log — журнал действий (может быть nil).
	Log func(line string)
	// Settle — пауза после остановки/старта, чтобы сеть и драйвер
	// устоялись (nil — без паузы; в тестах — пусто).
	Settle func()
}

// Run выполняет сценарий автопроверки домена. running — работает ли
// обход прямо сейчас: без работающего обхода сравнивать нечего.
func (f Flow) Run(raw string, running bool) (Result, error) {
	var res Result

	domain, testURL, err := Normalize(raw)
	if err != nil {
		return res, err
	}
	res.Domain, res.Url = domain, testURL

	if !running {
		return res, errors.New("обход выключен — включите zapret: проверка сравнивает «с обходом» и «без»")
	}
	if f.Test == nil || f.Stop == nil || f.Restart == nil || f.Apply == nil {
		return res, errors.New("domaincheck: не заданы обязательные колбэки")
	}
	logf := f.Log
	if logf == nil {
		logf = func(string) {}
	}
	settle := f.Settle
	if settle == nil {
		settle = func() {}
	}

	// 1. Проверка при текущем (работающем) обходе.
	logf("[домен] " + domain + ": проверяю с обходом…")
	res.WithBypass = f.Test(testURL)

	// 2. Обход останавливаем и проверяем «как есть».
	logf("[домен] " + domain + ": останавливаю обход для проверки без него…")
	if err := f.Stop(); err != nil {
		if rerr := f.Restart(); rerr != nil {
			return res, fmt.Errorf("не удалось остановить обход (%v) и поднять обратно (%v) — нажмите «Запустить»", err, rerr)
		}
		return res, fmt.Errorf("не удалось остановить обход для проверки: %v", err)
	}
	settle()
	res.WithoutBypass = f.Test(testURL)

	// 3. Решение: маскировать / исключить / ничего. «Доступен» — это в том
	// числе живой сайт с бот-защитой (HTTP 403/5xx): DPI так не отвечает.
	withOK := Reachable(res.WithBypass)
	withoutOK := Reachable(res.WithoutBypass)
	res.Action, res.Label, res.Message = Decide(withOK, withoutOK)
	res.Blocked = !withOK && !withoutOK
	res.Message += answerNote(res.WithBypass, "с обходом")
	res.Message += answerNote(res.WithoutBypass, "без обхода")

	// 4. Применяем режим (только при необходимости).
	if res.Action != ActionNothing {
		added, movedFrom, err := f.Apply(string(res.Action), domain)
		if err != nil {
			if rerr := f.Restart(); rerr != nil {
				return res, fmt.Errorf("список не сохранён (%v), обход не поднялся (%v) — нажмите «Запустить»", err, rerr)
			}
			return res, fmt.Errorf("не удалось сохранить список: %v", err)
		}
		res.Applied, res.MovedFrom = added, movedFrom
		if !added {
			// домен уже в этом списке: менять нечего, но перезапуск всё
			// равно нужен (мы сами остановили обход ради проверки)
			res.Message += " (домен уже был в этом списке)"
		}
	}

	// 5. Безусловный перезапуск: обход остановлен нами, изменения
	// списков вступают в силу только при старте winws.
	logf("[домен] " + domain + ": перезапускаю обход…")
	if err := f.Restart(); err != nil {
		return res, fmt.Errorf("обход остановлен, но не поднялся: %v — нажмите «Запустить»", err)
	}
	res.Restarted = true

	// 6. Проверяем, что автолечение действительно помогло.
	if res.Applied {
		settle()
		v := f.Test(testURL)
		res.Verify = &v
		res.Healed = Reachable(v)
		if !res.Healed {
			res.Message += " — но сайт так и не открылся: автолечение не помогло"
		}
	}

	return res, nil
}

// answerNote — пояснение, когда сайт отвечает кодом 4xx/5xx: для решения
// это «доступен» (живой сайт с защитой/ошибкой), но пользователю стоит
// видеть, почему вердикт именно такой.
func answerNote(r strattest.UrlTestResult, state string) string {
	if r.Ok || !Reachable(r) {
		return ""
	}
	return fmt.Sprintf(" (%s сайт отвечает HTTP %d — считаю доступным)", state, r.Status)
}
