package domaincheck

import (
	"testing"

	"zapret_gui/internal/strattest"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantURL string
		bad     bool
	}{
		{in: "example.com", want: "example.com", wantURL: "https://example.com/"},
		{in: "  Example.COM  ", want: "example.com", wantURL: "https://example.com/"},
		{in: "https://zendesk.com/hc/ru?x=1", want: "zendesk.com", wantURL: "https://zendesk.com/"},
		{in: "http://www.reddit.com/r/all", want: "www.reddit.com", wantURL: "https://www.reddit.com/"},
		{in: "example.com:8443", want: "example.com", wantURL: "https://example.com/"},
		{in: "example.com.", want: "example.com", wantURL: "https://example.com/"},
		{in: "", bad: true},
		{in: "   ", bad: true},
		{in: "localhost", bad: true},
		{in: "203.0.113.113", bad: true},
		{in: "203.0.113.113/32", bad: true},
		{in: "2001:db8::1", bad: true},
		{in: "^example.com", bad: true},
		{in: "*example.com", bad: true},
		{in: "пример.рф", bad: true}, // кириллицу нормализуем не здесь
	}
	for _, c := range cases {
		got, gotURL, err := Normalize(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("Normalize(%q): ожидалась ошибка, получено %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q): неожиданная ошибка: %v", c.in, err)
			continue
		}
		if got != c.want || gotURL != c.wantURL {
			t.Errorf("Normalize(%q) = (%q, %q), хотим (%q, %q)", c.in, got, gotURL, c.want, c.wantURL)
		}
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name          string
		with, without bool
		wantAction    Action
		wantLabel     string
	}{
		{"открывается в обоих состояниях", true, true, ActionNothing, ""},
		{"без обхода душится, с обходом ок", true, false, ActionGeneral, "маскировать"},
		{"с обходом ломается, без ок", false, true, ActionExclude, "исключить"},
		{"недоступен в обоих", false, false, ActionNothing, ""},
	}
	for _, c := range cases {
		action, label, msg := Decide(c.with, c.without)
		if action != c.wantAction || label != c.wantLabel {
			t.Errorf("%s: Decide(%v,%v) = (%q,%q), хотим (%q,%q)",
				c.name, c.with, c.without, action, label, c.wantAction, c.wantLabel)
		}
		if msg == "" {
			t.Errorf("%s: пустое сообщение пользователю", c.name)
		}
	}
}

func TestDecideBlockedMessageMentionsAutotreatment(t *testing.T) {
	action, _, msg := Decide(false, false)
	if action != ActionNothing {
		t.Fatalf("недоступен в обоих: ожидалось ActionNothing, получено %q", action)
	}
	if !contains(msg, "автолечение") {
		t.Errorf("сообщение должно объяснять, что автолечение не поможет: %q", msg)
	}
}

func TestReachable(t *testing.T) {
	cases := []struct {
		name string
		r    strattest.UrlTestResult
		want bool
	}{
		{"200 OK", strattest.UrlTestResult{Ok: true, Status: 200}, true},
		{"301 редирект", strattest.UrlTestResult{Ok: true, Status: 301}, true},
		{"403 бот-защита — сайт живой", strattest.UrlTestResult{Ok: false, Status: 403}, true},
		{"429 rate-limit — сайт живой", strattest.UrlTestResult{Ok: false, Status: 429}, true},
		{"500 ошибка сервера — сайт живой", strattest.UrlTestResult{Ok: false, Status: 500}, true},
		{"таймаут", strattest.UrlTestResult{Ok: false, Err: "context deadline exceeded"}, false},
		{"сброс соединения", strattest.UrlTestResult{Ok: false, Err: "connection forcibly closed"}, false},
		{"NXDOMAIN", strattest.UrlTestResult{Ok: false, Err: "no such host"}, false},
		{"пустой результат", strattest.UrlTestResult{}, false},
	}
	for _, c := range cases {
		if got := Reachable(c.r); got != c.want {
			t.Errorf("%s: Reachable(%+v) = %v, хотим %v", c.name, c.r, got, c.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
