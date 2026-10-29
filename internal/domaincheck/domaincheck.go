// Package domaincheck — «автолечение» пользовательских доменов: проверка
// сайта в двух состояниях (с обходом и без) и автоматический выбор режима
// «маскировать» / «исключить» / «ничего не делать».
//
// Здесь только чистые функции и сценарий с колбэками — без GUI, без
// Wails и без живой сети, поэтому всё покрывается обычными unit-тестами.
package domaincheck

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"

	"zapret_gui/internal/strattest"
)

// Action — режим пользовательского списка, выбранный автоматически.
type Action string

const (
	// ActionGeneral — «маскировать»: домен в list-general-user.txt.
	ActionGeneral Action = "general"
	// ActionExclude — «исключить»: домен в list-exclude-user.txt.
	ActionExclude Action = "exclude"
	// ActionNothing — ничего не добавлять (сайт и так в порядке или
	// недоступен в обоих состояниях).
	ActionNothing Action = "nothing"
)

// KindLabel — человекочитаемое имя режима/списка для интерфейса.
func KindLabel(kind string) string {
	switch kind {
	case "general":
		return "маскировать"
	case "exclude":
		return "исключить"
	case "ipset-exclude":
		return "исключить IP"
	default:
		return kind
	}
}

// Result — итог автопроверки домена. Уходит на фронтенд как JSON
// (Wails генерирует для структуры TypeScript-модель).
type Result struct {
	Domain        string                   `json:"domain"`
	Url           string                   `json:"url"`
	WithBypass    strattest.UrlTestResult  `json:"withBypass"`
	WithoutBypass strattest.UrlTestResult  `json:"withoutBypass"`
	Verify        *strattest.UrlTestResult `json:"verify,omitempty"`
	Action        Action                   `json:"action"`
	Label         string                   `json:"label"`
	Message       string                   `json:"message"`
	Applied       bool                     `json:"applied"`   // список изменён
	Restarted     bool                     `json:"restarted"` // обход перезапущен
	Healed        bool                     `json:"healed"`    // после применения сайт открылся
	Blocked       bool                     `json:"blocked"`   // недоступен в обоих состояниях
	MovedFrom     string                   `json:"movedFrom"` // перенесён из другого списка
}

// reDomain — доменное имя: буквы/цифры/дефисы, минимум одна точка.
var reDomain = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Normalize приводит пользовательский ввод к паре «домен + URL для проверки».
// Принимает «example.com», «https://example.com/path?x=1», «Example.COM»,
// отбрасывает путь/порт/завершающую точку. IP, CIDR и шаблоны zapret
// («^», «*») проверять нечем — для них возвращается понятная ошибка.
func Normalize(raw string) (domain, testURL string, err error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return "", "", errors.New("пустой домен")
	}
	if strings.ContainsAny(s, "*^") {
		return "", "", fmt.Errorf("нужен конкретный домен без «^» и «*» — шаблон проверить нельзя")
	}
	if strings.Contains(s, "://") {
		u, perr := url.Parse(s)
		if perr != nil || u.Host == "" {
			return "", "", fmt.Errorf("некорректный адрес %q", raw)
		}
		s = u.Host
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i] // путь и параметры для проверки не нужны
	}
	if h, _, perr := net.SplitHostPort(s); perr == nil {
		s = h
	}
	s = strings.TrimSuffix(s, ".")
	if strings.Contains(s, ":") {
		return "", "", fmt.Errorf("%q — это IP; проверка по IP не поддерживается, добавьте вручную", raw)
	}
	if net.ParseIP(s) != nil {
		return "", "", fmt.Errorf("%q — это IP; проверка по IP не поддерживается, добавьте вручную", raw)
	}
	if !reDomain.MatchString(s) {
		return "", "", fmt.Errorf("некорректный домен %q", raw)
	}
	return s, "https://" + s + "/", nil
}

// Reachable — сайт «доступен» для целей автолечения. Успешный ответ — да;
// осмысленный HTTP-код 4xx/5xx (бот-защита, ошибка сервера) — тоже да:
// это живой сайт, а не DPI-блокировка. Недоступен только тогда, когда
// соединения/DNS не было вовсе (таймаут, сброс, NXDOMAIN) — у таких
// результатов Err непустой либо Status==0.
func Reachable(r strattest.UrlTestResult) bool {
	if r.Ok {
		return true
	}
	if r.Err != "" {
		return false
	}
	return r.Status >= 400 && r.Status < 600
}

// Decide — решение по результатам двух проверок.
// Возвращает действие, подпись режима и сообщение для пользователя.
func Decide(withBypassOK, withoutBypassOK bool) (Action, string, string) {
	switch {
	case !withBypassOK && !withoutBypassOK:
		return ActionNothing, "",
			"недоступен и с обходом, и без — автолечение не поможет, списки не менялись"
	case !withoutBypassOK && withBypassOK:
		return ActionGeneral, KindLabel("general"),
			"без обхода не открывается — маскируем"
	case withoutBypassOK && !withBypassOK:
		return ActionExclude, KindLabel("exclude"),
			"без обхода открывается, с обходом ломается — исключаем"
	default:
		return ActionNothing, "",
			"открывается и с обходом, и без — трогать не нужно"
	}
}
