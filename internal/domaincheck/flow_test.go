package domaincheck

import (
	"errors"
	"testing"

	"zapret_gui/internal/strattest"
)

// fakeFlow — фейковое окружение: фиксируем порядок вызовов и результаты
// проверок, не трогая сеть и zapret. Test отдаёт with → without → verify
// по счётчику вызовов (сценарий всегда вызывает их в этом порядке).
type fakeFlow struct {
	with    strattest.UrlTestResult
	without strattest.UrlTestResult
	verify  strattest.UrlTestResult

	stopErr    error
	restartErr error
	applyErr   error
	applyAdded bool
	applyMoved string

	calls     []string
	testCalls int
}

func (f *fakeFlow) flow() Flow {
	return Flow{
		Test: func(url string) strattest.UrlTestResult {
			f.testCalls++
			switch f.testCalls {
			case 1:
				return f.with
			case 2:
				return f.without
			default:
				return f.verify
			}
		},
		Stop: func() error {
			f.calls = append(f.calls, "stop")
			return f.stopErr
		},
		Restart: func() error {
			f.calls = append(f.calls, "restart")
			return f.restartErr
		},
		Apply: func(kind, domain string) (bool, string, error) {
			f.calls = append(f.calls, "apply:"+kind+":"+domain)
			return f.applyAdded, f.applyMoved, f.applyErr
		},
		Log: func(string) {},
	}
}

func okResult() strattest.UrlTestResult {
	return strattest.UrlTestResult{Url: "https://example.com/", Ok: true, Status: 200, Ms: 120}
}

// failResult — «недоступен»: соединения не было вовсе.
func failResult() strattest.UrlTestResult {
	return strattest.UrlTestResult{Url: "https://example.com/", Ok: false, Err: "timeout"}
}

// blockedResult — живой сайт с бот-защитой/ошибкой сервера: для решения
// это «доступен» (DPI так не отвечает).
func botProtectedResult(status int) strattest.UrlTestResult {
	return strattest.UrlTestResult{Url: "https://example.com/", Ok: false, Status: status}
}

func TestRunGeneralWhenBlockedWithoutBypass(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyAdded: true, verify: okResult()}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Action != ActionGeneral || !res.Applied || !res.Restarted || !res.Healed {
		t.Fatalf("ожидалось general+applied+restarted+healed, получено %+v", res)
	}
	if want := []string{"stop", "apply:general:example.com", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("порядок вызовов %v, ожидался %v", f.calls, want)
	}
	if f.testCalls != 3 {
		t.Fatalf("ожидалось 3 проверки URL (с обходом, без, контроль), сделано %d", f.testCalls)
	}
}

func TestRunExcludeWhenBrokenByBypass(t *testing.T) {
	f := &fakeFlow{with: failResult(), without: okResult(), applyAdded: true, verify: okResult()}
	res, err := f.flow().Run("zendesk.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Action != ActionExclude || !res.Applied {
		t.Fatalf("ожидалось exclude+applied, получено %+v", res)
	}
	if want := []string{"stop", "apply:exclude:zendesk.com", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("порядок вызовов %v, ожидался %v", f.calls, want)
	}
}

func TestRunNothingWhenWorksBothWays(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: okResult()}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Action != ActionNothing || res.Applied {
		t.Fatalf("ожидалось nothing без изменений, получено %+v", res)
	}
	// обход всё равно поднимается обратно — мы его останавливали
	if want := []string{"stop", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("порядок вызовов %v, ожидался %v", f.calls, want)
	}
	if !res.Restarted {
		t.Fatal("обход должен быть перезапущен после проверки")
	}
	if f.testCalls != 2 {
		t.Fatalf("без изменений списков контрольная проверка не нужна, сделано %d", f.testCalls)
	}
}

func TestRunBlockedBothLeavesListsUntouched(t *testing.T) {
	f := &fakeFlow{with: failResult(), without: failResult()}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if !res.Blocked || res.Action != ActionNothing || res.Applied {
		t.Fatalf("ожидалось blocked+nothing, получено %+v", res)
	}
	if want := []string{"stop", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("списки не должны меняться: вызовы %v", f.calls)
	}
}

func TestRunVerifyFailedSaysAutotreatmentDidNotHelp(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyAdded: true, verify: failResult()}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Healed {
		t.Fatal("сайт не открылся — healed должен быть false")
	}
	if !contains(res.Message, "не помогло") {
		t.Fatalf("сообщение должно сказать «автолечение не помогло»: %q", res.Message)
	}
}

func TestRunOffBypassReturnsError(t *testing.T) {
	f := &fakeFlow{}
	if _, err := f.flow().Run("example.com", false); err == nil {
		t.Fatal("при выключенном обходе сравнение невозможно — ждали ошибку")
	}
	if len(f.calls) != 0 || f.testCalls != 0 {
		t.Fatalf("при выключенном обходе не должно быть побочных вызовов: %v", f.calls)
	}
}

func TestRunInvalidDomainReturnsError(t *testing.T) {
	f := &fakeFlow{}
	if _, err := f.flow().Run("203.0.113.113", true); err == nil {
		t.Fatal("IP нельзя проверять — ждали ошибку")
	}
	if len(f.calls) != 0 || f.testCalls != 0 {
		t.Fatalf("при некорректном домене не должно быть побочных вызовов: %v", f.calls)
	}
}

func TestRunStopFailureRestartsBypass(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: okResult(), stopErr: errors.New("access denied")}
	if _, err := f.flow().Run("example.com", true); err == nil {
		t.Fatal("ошибка остановки должна вернуться")
	}
	if want := []string{"stop", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("обход обязан подняться обратно: вызовы %v", f.calls)
	}
}

func TestRunApplyFailureRestartsBypass(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyErr: errors.New("disk full")}
	if _, err := f.flow().Run("example.com", true); err == nil {
		t.Fatal("ошибка записи списка должна вернуться")
	}
	if want := []string{"stop", "apply:general:example.com", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("после ошибки записи обход обязан подняться: вызовы %v", f.calls)
	}
}

func TestRunRestartFailureReported(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyAdded: true, restartErr: errors.New("net start failed")}
	res, err := f.flow().Run("example.com", true)
	if err == nil {
		t.Fatal("ошибка перезапуска должна вернуться")
	}
	if res.Restarted {
		t.Fatal("restarted=true при провале перезапуска")
	}
}

func TestRunAlreadyInListStillRestarts(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyAdded: false}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Applied {
		t.Fatal("домен уже был в списке — applied должен быть false")
	}
	if !contains(res.Message, "уже был") {
		t.Fatalf("сообщение должно уточнять, что домен уже был в списке: %q", res.Message)
	}
	// обход всё равно поднимается обратно
	if want := []string{"stop", "apply:general:example.com", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("порядок вызовов %v, ожидался %v", f.calls, want)
	}
}

func TestRunMovedFromOtherList(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyAdded: true, applyMoved: "exclude", verify: okResult()}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.MovedFrom != "exclude" {
		t.Fatalf("ожидался перенос из exclude, получено %q", res.MovedFrom)
	}
}

func TestRunSettleAfterStopAndBeforeVerify(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyAdded: true, verify: okResult()}
	settles := 0
	flow := f.flow()
	flow.Settle = func() { settles++ }
	if _, err := flow.Run("example.com", true); err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if settles != 2 {
		t.Fatalf(" settle должен вызываться после остановки и перед контролем: %d", settles)
	}
}

func TestRunWithoutCallbacksReturnsError(t *testing.T) {
	var zero Flow
	if _, err := zero.Run("example.com", true); err == nil {
		t.Fatal("без колбэков сценарий должен вернуть ошибку, а не паниковать")
	}
}

func TestRunApplyAndRestartFailureMentionsBypassDown(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(),
		applyErr: errors.New("disk full"), restartErr: errors.New("net start failed")}
	_, err := f.flow().Run("example.com", true)
	if err == nil {
		t.Fatal("двойная авария должна вернуть ошибку")
	}
	if !contains(err.Error(), "не поднялся") {
		t.Fatalf("в ошибке должно быть сказано, что обход не поднялся: %v", err)
	}
	if want := []string{"stop", "apply:general:example.com", "restart"}; !sameCalls(f.calls, want) {
		t.Fatalf("вызовы %v, ожидались %v", f.calls, want)
	}
}

func TestRunMovedFromKeepsVerifyFailure(t *testing.T) {
	f := &fakeFlow{with: failResult(), without: okResult(),
		applyAdded: true, applyMoved: "маскировать", verify: failResult()}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Verify == nil || res.Healed || res.MovedFrom != "маскировать" {
		t.Fatalf("ожидались MovedFrom + неудачный Verify: %+v", res)
	}
}

func TestRunBotProtectedCountsAsReachable(t *testing.T) {
	// с обходом сайт отвечает 403 (живой), без обхода — таймаут: значит
	// без обхода он недоступен → маскировать, а не «недоступен в обоих»
	f := &fakeFlow{with: botProtectedResult(403), without: failResult(), applyAdded: true, verify: okResult()}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Blocked || res.Action != ActionGeneral || !res.Applied {
		t.Fatalf("403 на живом сайте — это доступен: %+v", res)
	}
	if !contains(res.Message, "HTTP 403") {
		t.Fatalf("сообщение должно пояснять про HTTP 403: %q", res.Message)
	}
}

func TestRunBotProtectedBothDoesNothing(t *testing.T) {
	f := &fakeFlow{with: botProtectedResult(403), without: botProtectedResult(500)}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if res.Blocked || res.Action != ActionNothing {
		t.Fatalf("403/500 — сайт отвечает, значит доступен: %+v", res)
	}
}

func TestRunVerifyBotProtectedCountsAsHealed(t *testing.T) {
	f := &fakeFlow{with: okResult(), without: failResult(), applyAdded: true, verify: botProtectedResult(429)}
	res, err := f.flow().Run("example.com", true)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if !res.Healed {
		t.Fatalf("429 — сайт отвечает, лечение считать успешным: %+v", res)
	}
}

func sameCalls(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
