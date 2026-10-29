//go:build windows

package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"zapret_gui/internal/domaincheck"
	"zapret_gui/internal/strattest"
	"zapret_gui/internal/zapret"
)

// autoDomainRunning — автопроверка домена идёт: вторую параллельно не
// запускаем, и полный тест стратегий на это время блокируется (оба
// сценария останавливают обход).
var autoDomainRunning atomic.Bool

// settleDelay — пауза после остановки/старта обхода: WinDivert и сеть
// должны устояться, иначе проверка ловит переходное состояние.
const settleDelay = 1200 * time.Millisecond

// probeTimeout — таймаут одного замера URL. Замеров бывает два (см.
// probeUrl), поэтому он меньше, чем у ручной кнопки «Тест URL».
const probeTimeout = 6 * time.Second

// CheckAndAddDomain — кнопка «Проверить и добавить»: проверяет сайт с
// обходом и без, сам выбирает «маскировать» / «исключить» / ничего,
// пишет список и безусловно перезапускает zapret. Решение и порядок
// шагов живут в internal/domaincheck (покрыты unit-тестами), здесь —
// только связка с реальным zapret.
func (a *App) CheckAndAddDomain(raw string) (res domaincheck.Result, err error) {
	if !a.zap.Installed() {
		return res, errors.New("zapret не установлен")
	}
	if testRunning.Load() {
		return res, errors.New("идёт полный тест стратегий — дождитесь его окончания")
	}
	if !autoDomainRunning.CompareAndSwap(false, true) {
		return res, errors.New("автопроверка уже идёт")
	}
	defer autoDomainRunning.Store(false)
	// повторная проверка после захвата флага: полный тест мог стартовать
	// между первой проверкой и захватом
	if testRunning.Load() {
		return res, errors.New("идёт полный тест стратегий — дождитесь его окончания")
	}

	// Стратегию снимаем ДО остановки обхода: после Stop служба уже
	// STOPPED, и её стратегию из реестра не узнать — иначе перезапуск
	// молча пересоздал бы службу с другой стратегией.
	strategy := a.currentStrategy()
	stopped := false

	// Страховка: что бы ни случилось внутри сценария (ошибка, паника),
	// обход, остановленный ради проверки, обязан подняться обратно.
	defer func() {
		if p := recover(); p != nil {
			a.pushLog(fmt.Sprintf("[домен] паника в автопроверке: %v", p))
			err = fmt.Errorf("внутренняя ошибка автопроверки: %v", p)
		}
		if !stopped || a.zapRunning() {
			return
		}
		if rerr := a.RestartZapret(strategy); rerr != nil {
			a.pushLog("[домен] обход не поднялся после сбоя: " + rerr.Error())
			return
		}
		// страховка сработала — не пугаем пользователя советом «нажмите Запустить»
		a.pushLog("[домен] обход поднят страховкой после сбоя")
		if err != nil {
			err = errors.New(strings.Replace(err.Error(),
				" — нажмите «Запустить»", " (обход поднят страховкой автоматически)", 1))
		}
	}()

	f := domaincheck.Flow{
		Test: a.probeUrl,
		Stop: func() error {
			stopErr := a.StopZapret()
			if stopErr == nil {
				stopped = true
			}
			a.emitState() // интерфейс должен сразу увидеть «выключен»
			return stopErr
		},
		Restart: func() error {
			restartErr := a.RestartZapret(strategy)
			a.emitState()
			return restartErr
		},
		Apply:  a.applyAutoDomain,
		Log:    a.pushLog,
		Settle: func() { time.Sleep(settleDelay) },
	}
	res, err = f.Run(raw, a.zapRunning())
	a.emitState()
	return res, err
}

// probeUrl — проверка URL с подтверждением: одиночный замер нестабилен
// (транзиентные таймауты/сбросы), поэтому «недоступен» подтверждаем
// вторым замером. Успех не перепроверяем — это только тратит время.
func (a *App) probeUrl(url string) strattest.UrlTestResult {
	r := a.testUrlOnce(url)
	if domaincheck.Reachable(r) {
		return r
	}
	time.Sleep(250 * time.Millisecond)
	// второй замер — окончательный вердикт (успех или подтверждённый провал)
	return a.testUrlOnce(url)
}

// testUrlOnce — один замер URL с записью в журнал.
func (a *App) testUrlOnce(url string) strattest.UrlTestResult {
	r := strattest.TestUrl(url, probeTimeout)
	a.pushLog(fmt.Sprintf("[домен] %s → %s (%d мс)", r.Url, testVerdict(r), r.Ms))
	return r
}

// currentStrategy — стратегия работающего обхода: у службы она в реестре,
// у процесса — последняя выбранная в GUI. Вызывать ДО остановки обхода.
func (a *App) currentStrategy() string {
	if st, _ := zapret.ServiceState(); st == "RUNNING" || st == "START_PENDING" {
		if s := zapret.ServiceStrategy(); s != "" {
			return s
		}
	}
	return a.cfg.LastStrategy
}

// zapRunning — работает ли обход сейчас (служба или процесс).
func (a *App) zapRunning() bool {
	if st, _ := zapret.ServiceState(); st == "RUNNING" || st == "START_PENDING" {
		return true
	}
	return a.zap.ProcessRunning()
}

// applyAutoDomain записывает домен в выбранный список и убирает его из
// противоположного: держать домен одновременно в «маскировать» и
// «исключить» — противоречие. Возвращает (добавлено, «убран из»,
// ошибка).
func (a *App) applyAutoDomain(kind, domain string) (bool, string, error) {
	domains, err := a.zap.GetUserDomains()
	if err != nil {
		return false, "", err
	}

	other := ""
	switch kind {
	case string(domaincheck.ActionGeneral):
		other = string(domaincheck.ActionExclude)
	case string(domaincheck.ActionExclude):
		other = string(domaincheck.ActionGeneral)
	}

	updates := map[string][]string{}
	removedFrom := ""
	if other != "" {
		kept := make([]string, 0, len(domains[other]))
		for _, d := range domains[other] {
			if strings.EqualFold(d, domain) {
				removedFrom = domaincheck.KindLabel(other)
				continue
			}
			kept = append(kept, d)
		}
		if removedFrom != "" {
			updates[other] = kept
		}
	}

	added := false
	present := false
	for _, d := range domains[kind] {
		if strings.EqualFold(d, domain) {
			present = true
			break
		}
	}
	if !present {
		list := append(append([]string{}, domains[kind]...), domain)
		sort.Strings(list)
		updates[kind] = list
		added = true
	}

	if len(updates) == 0 {
		return false, "", nil // уже в нужном списке и в другом его нет
	}
	if err := a.zap.SetUserDomains(updates); err != nil {
		return false, "", err
	}
	if added {
		a.pushLog("[домен] " + domain + " → список «" + domaincheck.KindLabel(kind) + "»")
	} else {
		a.pushLog("[домен] " + domain + " уже в списке «" + domaincheck.KindLabel(kind) + "», убран из «" + removedFrom + "»")
	}
	return added, removedFrom, nil
}
