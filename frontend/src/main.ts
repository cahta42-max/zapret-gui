// Zapret GUI — логика фронтенда
import {
  GetStatus, GetLogs, EnsureTools, ReportFrontendError, FrontendPing,
  StartZapret, StopZapret, RestartZapret, InstallZapretService, RemoveZapretService,
  SelectStrategy, SetGameFilter, SetIpset, UpdateIpset, SetZapretCheckUpdates,
  SetZapretAutoRun, GetUserDomains, SetUserDomains,
  UpdateZapret, UpdateTgws,
  StartTgws, StopTgws, SetTgwsAutostart, OpenTgwsInTelegram,
  SetAutostartGui, SetStartMinimized, RunDiagnostics,
  SetupDefenderExclusion,
  OpenOriginalServiceBat, OpenOriginalStrategyBat, OpenZapretFolder, OpenTgwsFolder,
  TestUrl, StartStrategyTest, ZapretVersions, InstallZapretVersion,
  OldTgwsAutostart, RemoveOldTgwsAutostart,
  CheckAndAddDomain, GetAutoHeal, SetAutoHealEnabled, SetAutoHealDryRun, RestartAutoWatch,
} from '../wailsjs/go/main/App';
import { EventsOn, BrowserOpenURL } from '../wailsjs/runtime/runtime';

// перехват ошибок фронтенда — в журнал
window.addEventListener('error', (e) => {
  ReportFrontendError(`${e.message} @ ${e.filename ?? ''}:${e.lineno ?? 0}`);
});
window.addEventListener('unhandledrejection', (e) => {
  ReportFrontendError(String((e as PromiseRejectionEvent).reason));
});

// ------------------------------------------------------------------ helpers

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

let status: any = null;
const isMac = () => status?.platform === 'darwin';
const engineName = () => isMac() ? 'utunws' : 'zapret';
let setupDone = false;
let testInProgress = false;
let autoCheckInProgress = false; // автопроверка домена: управление обходом занято
let prevZapretRunning = false; // детект рестарта (off→on) для скрытия «не применено»

function toast(msg: string, ok = false) {
  const el = $('toast');
  el.textContent = msg;
  el.className = 'toast' + (ok ? ' ok' : '');
  setTimeout(() => el.classList.add('hidden'), ok ? 2500 : 5000);
}

async function tryRun(fn: () => Promise<any>, okMsg?: string) {
  try {
    await fn();
    if (okMsg) toast(okMsg, true);
  } catch (e: any) {
    toast(String(e?.message ?? e));
  }
}

// ------------------------------------------------------------------ рендер

function setPill(pillId: string, on: boolean, warn = false, textOn = 'работает', textOff = 'выключен') {
  const pill = $<HTMLElement>(pillId);
  pill.textContent = on ? textOn : textOff;
  pill.className = 'pill' + (on ? ' pill-on' : warn ? ' pill-warn' : '');
}

function render() {
  if (!status) return;

  document.body.classList.toggle('platform-mac', isMac());
  $('admin-banner').classList.toggle('hidden', status.admin || isMac());
  $('chip-app').textContent = 'gui ' + status.appVersion;
  $('chip-zapret').textContent = status.zapret.installed
    ? engineName() + ' ' + (status.zapret.version || '?') : engineName() + ' —';
  $('chip-tgws').textContent = status.tgws.installed
    ? 'tg-ws ' + (status.tgws.version || '?') : 'tg-ws —';

  // нормализация версии для сравнения: «v1.10.0» и «1.10.0.0» — одна версия
  const ver = (s: string): string => {
    let v = (s || '').replace(/^v/, '');
    const parts = v.split('.');
    if (parts.length === 4 && parts[3] === '0') v = parts.slice(0, 3).join('.');
    return v;
  };

  // обновления доступны?
  const chipZ = $('chip-zapret'), chipT = $('chip-tgws');
  chipZ.classList.toggle('update-available',
    !!(status.zapret.latestVersion && status.zapret.installed &&
    status.zapret.version && ver(status.zapret.version) !== ver(status.zapret.latestVersion)));
  chipT.classList.toggle('update-available',
    !!(status.tgws.latestVersion && status.tgws.installed &&
    status.tgws.version && ver(status.tgws.version) !== ver(status.tgws.latestVersion)));

  // ---------------- zapret
  const z = status.zapret;
  const zInstalled = z.installed;
  $('card-zapret').style.opacity = zInstalled ? '1' : '1'; // карточка кликабельна и без движка
  ($('zapret-update-btn') as HTMLButtonElement).disabled = !zInstalled;

  setPill('pill-zapret', z.running);
  $('dot-zapret').className = 'dot' + (z.running ? ' on' : '');
  $('zapret-mode').textContent = z.running
    ? (isMac() ? 'движок' : (z.mode === 'service' ? 'служба Windows' : 'процесс')) + (z.currentStrategy ? ' · ' + z.currentStrategy : '')
    : '';

  // стратегии
  const sel = $<HTMLSelectElement>('strategy-select');
  const strategies: string[] = (z.strategies || []).map((s: any) => s.name);
  if (sel.dataset.count !== String(strategies.length)) {
    sel.innerHTML = strategies.map(n => `<option value="${n}">${n}</option>`).join('');
    sel.dataset.count = String(strategies.length);
  }
  const wanted = z.currentStrategy || (strategies.includes('general') ? 'general' : strategies[0]);
  if (wanted && sel.value !== wanted) sel.value = wanted;

  const mainBtn = $<HTMLButtonElement>('zapret-main-btn');
  mainBtn.textContent = !zInstalled ? 'Установить' : (z.running ? 'Остановить' : 'Запустить');
  mainBtn.title = zInstalled
    ? (z.running
      ? 'Остановить ' + engineName() + (isMac() ? ' (системный туннель закроется)' : ' (служба и процессы)')
      : 'Запустить ' + engineName() + ' — ' + (z.autoRun
          ? (isMac() ? 'системный движок (автозапуск при загрузке и старте GUI)' : 'службой Windows (автозапуск при старте системы)')
          : 'как процесс (автозапуск выключен)'))
    : 'Скачать и установить ' + engineName() + (isMac() ? ' (потребуется пароль администратора)' : '');
  // «Остановить» — красная, «Запустить/Установить» — голубая
  mainBtn.classList.toggle('btn-danger', zInstalled && z.running);
  mainBtn.classList.toggle('btn-primary', !(zInstalled && z.running));

  // «Перезапустить» имеет смысл только работающему zapret
  ($('zapret-restart-btn') as HTMLButtonElement).disabled = !z.running || autoCheckInProgress;
  // на время автопроверки домена обход останавливается — кнопки блокируем,
  // чтобы не было двух хозяев у службы
  mainBtn.disabled = autoCheckInProgress;
  // рестарт (переход выключен → работает) применяет свои домены
  if (!prevZapretRunning && z.running) {
    const pill = document.getElementById('domains-dirty-pill');
    if (pill) pill.classList.add('hidden');
  }
  prevZapretRunning = z.running;

  // сегменты
  setActiveSeg('seg-game', z.gameFilter);
  setActiveSeg('seg-ipset', z.ipset);
  ($('chk-zapret-updates') as HTMLInputElement).checked = z.checkUpdatesFlag;
  ($('chk-zapret-autorun') as HTMLInputElement).checked = z.autoRun;

  // ---------------- tgws
  const t = status.tgws;
  $('card-tgws').style.opacity = t.installed ? '1' : '.55';
  ($('tgws-main-btn') as HTMLButtonElement).disabled = !t.installed;
  ($('tgws-open-btn') as HTMLButtonElement).disabled = !t.installed;

  setPill('pill-tgws', t.running);
  $('dot-tgws').className = 'dot' + (t.running ? ' on' : '');
  $('tgws-port').textContent = t.running
    ? (t.portOpen ? 'порт 1443 · подключён' : 'порт 1443 · ждёт') : '';

  const tMainBtn = $<HTMLButtonElement>('tgws-main-btn');
  tMainBtn.textContent = t.running ? 'Остановить' : 'Запустить';
  tMainBtn.classList.toggle('btn-danger', t.running);
  tMainBtn.classList.toggle('btn-primary', !t.running);
  ($('chk-tgws-autostart') as HTMLInputElement).checked = t.autostart;

  // настройки
  $('link-gh-zapret').textContent = engineName() + ' ↗';
  ($('chk-autostart-gui') as HTMLInputElement).checked = status.autostartGui;
  $('settings-tools-dir').textContent = status.toolsDir;
}

function setActiveSeg(segId: string, value: string) {
  const seg = $<HTMLElement>(segId);
  seg.querySelectorAll('button').forEach(b =>
    b.classList.toggle('active', (b as HTMLElement).dataset.v === value));
}

// ------------------------------------------------------------------ адаптация окна
// Окно не подгоняется под контент автоматически: контент скроллится внутри
// окна (body { overflow-y: auto }). Это предсказуемо и никогда не обрезает
// низ (футер со ссылками).

// ------------------------------------------------------------------ журнал

function appendLog(line: string) {
  const log = $('log');
  const div = document.createElement('div');
  // цветовая маркировка потоков: глаз должен ловить источники мгновенно
  let cls = 'ln';
  const m = line.match(/^\[(zapret|tg-ws|test|ui|err)[^\]]*\]/i);
  const tag = m ? m[1].toLowerCase() : '';
  if (/ошибка|сбой|✗|failed|не удалось/i.test(line)) cls += ' bad';
  else if (tag === 'zapret') cls += ' zap';
  else if (tag === 'tg-ws') cls += ' tg';
  else if (tag === 'test') cls += ' tst';
  else if (tag) cls += ' sys';
  if (/→ OK \d/.test(line)) cls += ' ok-frag';
  div.className = cls;
  div.textContent = line;
  log.appendChild(div);
  while (log.childElementCount > 800 && log.firstElementChild) {
    log.firstElementChild.remove();
  }
  log.scrollTop = log.scrollHeight;
}

// ------------------------------------------------------------------ первый запуск

async function firstRun() {
  $('overlay-setup').classList.remove('hidden');
  try {
    await EnsureTools();
    toast('Компоненты установлены', true);
  } catch (e: any) {
    toast('Ошибка загрузки: ' + String(e?.message ?? e));
  }
  $('overlay-setup').classList.add('hidden');
}

function showProgress(p: any) {
  const map: Record<string, { bar: string; txt: string }> = {
    zapret: { bar: 'bar-zapret', txt: 'bar-zapret-txt' },
    tgws: { bar: 'bar-tgws', txt: 'bar-tgws-txt' },
  };
  // если открыта общая модалка прогресса (обновление) — рисуем и там
  const generic = $('overlay-progress');
  if (!generic.classList.contains('hidden')) {
    $('bar-generic').style.width = Math.round(p.pct * 100) + '%';
    $('progress-stage').textContent = p.stage || '';
  }
  const target = map[p.task];
  if (!target) return;
  const bar = $<HTMLElement>(target.bar);
  bar.style.width = Math.round(p.pct * 100) + '%';
  if (p.pct >= 1) bar.classList.add('done');
  $(target.txt).textContent = p.stage || '';
}

// ------------------------------------------------------------------ события

async function refresh() {
  try {
    status = await GetStatus();
    render();
  } catch (e: any) {
    // не глотаем молча — иначе мёртвый статус выглядит как «фича»
    appendLog('[refresh] ошибка обновления: ' + String(e?.message ?? e));
  }
}

// ------------------------------------------------------------------ тест стратегий

function renderTestUrlResult(r: any) {
  const el = $('test-url-result');
  el.classList.remove('hidden', 'ok', 'bad');
  el.classList.add(r.ok ? 'ok' : 'bad');
  el.textContent = r.ok
    ? `✓ ${r.url} — HTTP ${r.status}, ${r.ms} мс`
    : `✗ ${r.url} — ${r.err || 'HTTP ' + r.status}, ${r.ms} мс`;
}

async function renderTestDone(suite: any) {
  testInProgress = false;
  $('test-progress-wrap').classList.add('hidden');
  ($('test-full-btn') as HTMLButtonElement).disabled = false;

  const results = suite.results || [];
  if (!results.length) {
    toast('Тест не дал результатов — смотрите журнал');
    return;
  }

  const tbody = $('test-tbody');
  tbody.innerHTML = results.map((r: any) => {
    const fail = Math.max(0, (r.total ?? 0) - (r.ok ?? 0));
    return `<tr class="${r.strategy === suite.recommended ? 'recommended' : ''}">
      <td>${r.strategy}</td>
      <td class="c-ok">${r.ok ?? '—'}</td>
      <td class="c-bad">${fail || '—'}</td>
      <td class="c-warn">${r.blocked ? '⚠ ' + r.blocked : '—'}</td>
      <td><button class="btn btn-mini apply-btn" data-strategy="${r.strategy}">запустить</button></td>
    </tr>`;
  }).join('');

  tbody.querySelectorAll('.apply-btn').forEach((b) => {
    (b as HTMLElement).onclick = () =>
      tryRun(
        () => isMac()
          ? StartZapret((b as HTMLElement).dataset.strategy!)
          : InstallZapretService((b as HTMLElement).dataset.strategy!),
        (isMac() ? 'Движок запущен: ' : 'Служба запущена: ') + (b as HTMLElement).dataset.strategy);
  });

  const rec = $('test-rec');
  if (suite.recommended) {
    rec.classList.remove('hidden');
    rec.innerHTML = `Рекомендация для вашей сети: <b>${suite.recommended}</b>
      <button class="btn btn-mini" id="test-apply-rec">применить</button>`;
    $('test-apply-rec').onclick = () =>
      tryRun(
        () => isMac() ? StartZapret(suite.recommended) : InstallZapretService(suite.recommended),
        (isMac() ? 'Движок запущен: ' : 'Служба запущена: ') + suite.recommended);
  } else {
    rec.classList.add('hidden');
  }
  $('test-results').classList.remove('hidden');

  // результаты пришли — раскрываем раздел целиком (иначе «щелочка»)
  const body = $('test-body');
  const toggle = $<HTMLButtonElement>('test-toggle');
  if (body.classList.contains('hidden')) {
    body.classList.remove('hidden');
    toggle.textContent = 'свернуть';
  }
  const scroll = document.querySelector('.test-scroll');
  if (scroll) scroll.scrollTop = 0;
}

// ------------------------------------------------------------------ init

document.addEventListener('DOMContentLoaded', async () => {
  FrontendPing();

  EventsOn('log', (line: string) => appendLog(line));
  EventsOn('state', () => refresh());
  EventsOn('progress', (p: any) => showProgress(p));
  EventsOn('test-progress', (p: any) => {
    const wrap = $('test-progress-wrap');
    wrap.classList.remove('hidden');
    $('test-progress-label').textContent =
      `тестирую «${p.strategy}» (${p.done + 1}/${p.total})…`;
    $('test-progress-bar').style.width = Math.round(p.done / p.total * 100) + '%';
  });
  EventsOn('test-done', (suite: any) => renderTestDone(suite));

  // история журнала
  for (const l of await GetLogs()) appendLog(l);

  await refresh();
  if (!setupDone && status?.firstRun) {
    setupDone = true;
    await firstRun();
    await refresh();
  }
  setInterval(() => { refresh(); void refreshAutoHeal(); }, 2000);

  // ---------------- zapret: кнопки
  // Единая кнопка: «Запустить» ставит службу (если включён автозапуск)
  // или стартует процесс; «Остановить» прибирает и то, и другое.
  $('zapret-main-btn').onclick = () => {
    // движка нет (или снят) — установка/повтор установки после ошибки
    if (!status.zapret.installed) {
      (async () => { await firstRun(); await refresh(); })();
      return;
    }
    if (status.zapret.running) { tryRun(StopZapret); return; }
    const strategy = $<HTMLSelectElement>('strategy-select').value;
    if (isMac())
      tryRun(() => StartZapret(strategy));
    else if (status.zapret.autoRun)
      tryRun(() => InstallZapretService(strategy), 'Служба zapret запущена');
    else
      tryRun(() => StartZapret(strategy));
  };

  $('strategy-select').onchange = () => {
    tryRun(() => SelectStrategy($<HTMLSelectElement>('strategy-select').value));
  };

  // перезапуск zapret (служба или процесс) — с текущей стратегией из селекта
  $('zapret-restart-btn').onclick = () => {
    const strategy = $<HTMLSelectElement>('strategy-select').value;
    tryRun(() => RestartZapret(strategy), 'zapret перезапущен');
  };

  // ---------------- вкладки: zapret / telegram
  document.querySelectorAll<HTMLButtonElement>('#tabbar .tab').forEach(btn => {
    btn.onclick = () => {
      document.querySelectorAll('#tabbar .tab').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      const tab = btn.dataset.tab;
      $('tab-zapret').classList.toggle('hidden', tab !== 'zapret');
      $('card-test').classList.toggle('hidden', tab !== 'zapret');
      $('tab-tgws').classList.toggle('hidden', tab !== 'tgws');
    };
  });

  // ---------------- свои домены: управляемые списки (не raw-файлы)
  type Kind = 'general' | 'exclude' | 'ipset-exclude';
  const kindLabels: Record<Kind, string> = {
    'general': 'маскировать',
    'exclude': 'исключить',
    'ipset-exclude': 'исключить IP',
  };
  const kindInputHints: Record<Kind, string> = {
    'general': 'example.com',
    'exclude': 'example.com',
    'ipset-exclude': '192.0.2.1 или 203.0.113.0/24',
  };
  let domains: Record<Kind, string[]> = { 'general': [], 'exclude': [], 'ipset-exclude': [] };

  const renderDomains = () => {
    const wrap = $('domain-groups');
    wrap.innerHTML = '';
    (Object.keys(kindLabels) as Kind[]).forEach(kind => {
      const group = document.createElement('div');
      group.className = 'dgroup';
      const label = document.createElement('span');
      label.className = 'dgroup-label';
      label.textContent = kindLabels[kind];
      group.appendChild(label);

      const chips = document.createElement('div');
      chips.className = 'dchips';
      if (!domains[kind].length) {
        const empty = document.createElement('span');
        empty.className = 'dchips-empty';
        empty.textContent = 'пусто';
        chips.appendChild(empty);
      }
      domains[kind].forEach(domain => {
        const chip = document.createElement('span');
        chip.className = 'dchip' + (kind === 'general' ? ' dchip-green' : '');
        chip.title = kind === 'ipset-exclude' ? domain : domain;
        const text = document.createElement('span');
        text.textContent = domain;
        const x = document.createElement('i');
        x.className = 'dchip-x';
        x.textContent = '×';
        x.title = 'Убрать ' + domain;
        x.onclick = () => {
          domains[kind] = domains[kind].filter(d => d !== domain);
          persistDomains();
        };
        chip.appendChild(text);
        chip.appendChild(x);
        chips.appendChild(chip);
      });
      group.appendChild(chips);
      wrap.appendChild(group);
    });
  };

  const setDomainsDirty = (dirty: boolean) => {
    const pill = document.getElementById('domains-dirty-pill');
    if (pill) pill.classList.toggle('hidden', !dirty);
  };

  const persistDomains = async () => {
    try {
      await SetUserDomains({ ...domains });
      renderDomains();
      setDomainsDirty(true);
    } catch (e: any) {
      toast(String(e?.message ?? e));
    }
  };

  const loadDomains = async () => {
    try {
      const got = await GetUserDomains();
      domains = {
        'general': got['general'] || [],
        'exclude': got['exclude'] || [],
        'ipset-exclude': got['ipset-exclude'] || [],
      };
      renderDomains();
    } catch { /* карточка не критична */ }
  };
  loadDomains();

  const addDomain = () => {
    const kind = $<HTMLSelectElement>('domain-action').value as Kind;
    const input = $<HTMLInputElement>('domain-input');
    const domain = input.value.trim().toLowerCase();
    if (!domain) { toast('Введите домен'); return; }
    if (domains[kind].includes(domain)) { toast('Такой домен уже есть в этом списке'); return; }
    if (domain.includes(' ') || /[^a-z0-9.^*_\-:/.]/.test(domain)) {
      toast('Недопустимые символы в домене');
      return;
    }
    domains[kind] = [...domains[kind], domain].sort();
    input.value = '';
    persistDomains();
  };
  $('domain-add-btn').onclick = addDomain;
  $('domain-input').addEventListener('keydown', e => {
    if (e.key === 'Enter') addDomain();
  });

  // ---------------- автопроверка домена: с обходом / без → режим → перезапуск
  const fmtProbe = (p: any) => {
    if (!p) return '—';
    if (p.ok) return `открывается (${p.status}, ${p.ms} мс)`;
    return p.err ? `не открывается (${p.err})` : `не открывается (HTTP ${p.status})`;
  };

  const renderDomainCheck = (r: any) => {
    const el = $('domain-check-result');
    el.classList.remove('hidden');
    const ok = r.applied ? r.healed : !r.blocked;
    el.className = ok ? 'ok' : 'bad';
    const parts = [
      `${r.domain}:`,
      `с обходом — ${fmtProbe(r.withBypass)}`,
      `без обхода — ${fmtProbe(r.withoutBypass)}`,
      `решение: ${r.message}`,
    ];
    if (r.movedFrom) parts.push(`убран из «${r.movedFrom}»`);
    else if (r.applied) parts.push('список обновлён');
    if (r.restarted) parts.push('обход перезапущен');
    if (r.verify) parts.push(`контроль — ${fmtProbe(r.verify)}`);
    el.textContent = parts.join(' · ');
  };

  const autoCheckDomain = async () => {
    const input = $<HTMLInputElement>('domain-input');
    const raw = input.value.trim();
    if (!raw) { toast('Введите домен'); return; }
    if (testInProgress) { toast('Идёт полный тест стратегий — дождитесь окончания'); return; }
    const btn = $<HTMLButtonElement>('domain-autocheck-btn');
    const label = btn.textContent || 'Проверить и добавить';
    autoCheckInProgress = true;
    btn.disabled = true;
    btn.textContent = 'проверяю…';
    ($('domain-add-btn') as HTMLButtonElement).disabled = true;
    const box = $('domain-check-result');
    box.classList.remove('hidden');
    box.className = '';
    box.textContent = `проверяю ${raw}: сначала с обходом, потом без него — обход будет выключен на несколько секунд, проверка занимает до минуты…`;
    try {
      const r: any = await CheckAndAddDomain(raw);
      renderDomainCheck(r);
      input.value = '';
      // обход перезапущен → все списки (в т.ч. ручные правки) уже применены,
      // поэтому «не применено» гасим даже когда новых записей не появилось
      if (r.restarted) setDomainsDirty(false);
      // applied — список дополнен; movedFrom — домен убран из противоположного
      // списка (added при этом false): чипы надо перечитать в обоих случаях
      if (r.applied || r.movedFrom) await loadDomains();
      toast(r.message, r.applied ? r.healed : !r.blocked);
    } catch (e: any) {
      box.className = 'bad';
      box.textContent = String(e?.message ?? e);
      toast(String(e?.message ?? e));
    } finally {
      autoCheckInProgress = false;
      btn.disabled = false;
      btn.textContent = label;
      ($('domain-add-btn') as HTMLButtonElement).disabled = false;
      await refresh(); // вернуть активность кнопкам обхода
    }
  };
  $('domain-autocheck-btn').onclick = autoCheckDomain;

  // ---------------- автолечение: наблюдение за браузером (Windows)
  const chkAutoHeal = $<HTMLInputElement>('chk-autoheal');
  const chkAutoHealDry = $<HTMLInputElement>('chk-autoheal-dry');
  const renderAutoHeal = (s: any) => {
    chkAutoHeal.checked = !!s.enabled;
    chkAutoHealDry.checked = !!s.dryRun;
    $('autoheal-dry-row').classList.toggle('hidden', !s.enabled);
    chkAutoHealDry.disabled = !s.enabled;
    const hint = $('autoheal-hint');
    if (!s.enabled) {
      hint.textContent = 'выключено — домены правятся только вручную';
    } else if (s.active) {
      hint.textContent = `наблюдение работает: ${s.mode}. Сработавшие домены — в журнале (строки «автолечение»).`;
    } else if (s.lastError) {
      hint.textContent = `наблюдение не запущено: ${s.lastError}`;
    } else {
      hint.textContent = 'наблюдение запускается… если статус не меняется — нажмите «перезапустить наблюдение»';
    }
    const btnRestart = $<HTMLButtonElement>('autoheal-restart-btn');
    btnRestart.classList.toggle('hidden', !(s.enabled && !s.active));
    btnRestart.disabled = !s.enabled || s.active;
  };
  const refreshAutoHeal = async () => {
    try { renderAutoHeal(await GetAutoHeal()); } catch { /* mac-версия: заглушка */ }
  };
  chkAutoHeal.onchange = async () => {
    try {
      await SetAutoHealEnabled(chkAutoHeal.checked);
      toast(chkAutoHeal.checked ? 'Автолечение включено' : 'Автолечение выключено');
    } catch (e: any) { toast(String(e?.message ?? e)); }
    await refreshAutoHeal();
  };
  chkAutoHealDry.onchange = async () => {
    try {
      await SetAutoHealDryRun(chkAutoHealDry.checked);
      toast(chkAutoHealDry.checked
        ? 'Режим журнала: списки не меняются'
        : 'Автолечение: списки будут меняться автоматически');
    } catch (e: any) { toast(String(e?.message ?? e)); }
    await refreshAutoHeal();
  };
  $<HTMLButtonElement>('autoheal-restart-btn').onclick = async (ev) => {
    const btn = ev.currentTarget as HTMLButtonElement;
    btn.disabled = true;
    try {
      await RestartAutoWatch();
      toast('Наблюдение перезапущено', true);
    } catch (e: any) { toast(String(e?.message ?? e)); }
    await refreshAutoHeal();
    btn.disabled = false;
  };
  refreshAutoHeal();

  $<HTMLSelectElement>('domain-action').onchange = () => {
    const kind = $<HTMLSelectElement>('domain-action').value as Kind;
    $<HTMLInputElement>('domain-input').placeholder = kindInputHints[kind];
  };

  // удаление службы — в настройках
  $('zapret-remove-svc-btn').onclick = () => {
    if (status.zapret.serviceState)
      tryRun(RemoveZapretService, 'Служба zapret удалена');
    else
      toast('Служба не установлена');
  };

  $('zapret-update-btn').onclick = async () => {
    $('overlay-progress').classList.remove('hidden');
    $('progress-title').textContent = 'Обновление zapret…';
    $('bar-generic').style.width = '0%';
    await tryRun(UpdateZapret, 'zapret обновлён');
    $('overlay-progress').classList.add('hidden');
    await refresh();
  };

  $('seg-game').querySelectorAll('button').forEach(b => {
    b.onclick = () => tryRun(() => SetGameFilter((b as HTMLElement).dataset.v!), 'Применится после перезапуска zapret');
  });
  $('seg-ipset').querySelectorAll('button').forEach(b => {
    b.onclick = () => tryRun(() => SetIpset((b as HTMLElement).dataset.v!));
  });
  $('ipset-update-btn').onclick = () => tryRun(UpdateIpset, 'Список ipset обновлён');

  $('chk-zapret-updates').onchange = (e) =>
    tryRun(() => SetZapretCheckUpdates((e.target as HTMLInputElement).checked));

  $('chk-zapret-autorun').onchange = (e) =>
    tryRun(() => SetZapretAutoRun((e.target as HTMLInputElement).checked));

  $('open-service-bat').onclick = (e) => { e.preventDefault(); tryRun(OpenOriginalServiceBat); };
  $('open-strategy-bat').onclick = (e) => {
    e.preventDefault();
    tryRun(() => OpenOriginalStrategyBat($<HTMLSelectElement>('strategy-select').value));
  };
  $('open-zapret-folder').onclick = (e) => { e.preventDefault(); tryRun(OpenZapretFolder); };

  // ---------------- tgws: кнопки
  $('tgws-main-btn').onclick = () =>
    status.tgws.running ? tryRun(StopTgws) : tryRun(StartTgws);

  $('tgws-update-btn').onclick = () =>
    tryRun(UpdateTgws, 'tg-ws-proxy обновлён');

  $('tgws-open-btn').onclick = () => tryRun(OpenTgwsInTelegram);

  $('chk-tgws-autostart').onchange = (e) =>
    tryRun(() => SetTgwsAutostart((e.target as HTMLInputElement).checked));

  $('open-tgws-folder').onclick = (e) => { e.preventDefault(); tryRun(OpenTgwsFolder); };

  // ---------------- тест стратегий
  $('test-toggle').onclick = () => {
    const body = $('test-body');
    const btn = $<HTMLButtonElement>('test-toggle');
    body.classList.toggle('hidden');
    btn.textContent = body.classList.contains('hidden') ? 'развернуть' : 'свернуть';
  };

  $('test-url-btn').onclick = async () => {
    const url = ($('test-url-input') as HTMLInputElement).value.trim();
    if (!url) { toast('Введите URL для проверки'); return; }
    $('test-url-result').classList.remove('hidden');
    $('test-url-result').textContent = `проверяю ${url}…`;
    renderTestUrlResult(await TestUrl(url));
  };

  $('test-full-btn').onclick = async () => {
    if (testInProgress) return;
    if (!status?.zapret?.installed) { toast('Сначала установите ' + engineName()); return; }
    testInProgress = true;
    ($('test-full-btn') as HTMLButtonElement).disabled = true;
    $('test-results').classList.add('hidden');
    $('test-url-result').classList.add('hidden');
    // ход прогона виден в журнале — открываем его сразу, не заставляя искать
    $('console-wrap').classList.remove('hidden');
    const custom = ($('test-url-input') as HTMLInputElement).value.trim();
    tryRun(() => StartStrategyTest(custom), undefined);
  };

  // ---------------- журнал: скрыт по умолчанию, открывается кнопкой «логи»
  $('btn-logs').onclick = () => $('console-wrap').classList.remove('hidden');
  $('btn-close-log').onclick = () => $('console-wrap').classList.add('hidden');
  $('btn-clear-log').onclick = () => { $('log').innerHTML = ''; };

  // ---------------- ссылки GitHub
  $('link-gh-zapret').onclick = (e) => {
    e.preventDefault();
    BrowserOpenURL(isMac()
      ? 'https://github.com/Flowseal/zapret-mac-discord-youtube'
      : 'https://github.com/Flowseal/zapret-discord-youtube');
  };
  $('link-gh-tgws').onclick = (e) => {
    e.preventDefault();
    BrowserOpenURL('https://github.com/Flowseal/tg-ws-proxy');
  };

  // ---------------- версии zapret
  $('zapret-versions-btn').onclick = async () => {
    $('overlay-versions').classList.remove('hidden');
    $('versions-list').innerHTML = '<p class="modal-text">Загружаю список версий с GitHub…</p>';
    const versions = await ZapretVersions();
    const current = status?.zapret?.version || '';
    if (!versions.length) {
      $('versions-list').innerHTML = '<p class="modal-text">Не удалось получить список (нет доступа к github.com?).</p>';
      return;
    }
    $('versions-list').innerHTML = versions.map(v => `
      <div class="ver-row">
        <span class="ver-tag">${v}</span>
        ${v === current ? '<span class="ver-cur">● установлена</span>' : ''}
        ${v !== current ? `<button class="btn btn-mini" data-ver="${v}">установить</button>` : ''}
      </div>`).join('');
    $('versions-list').querySelectorAll('button[data-ver]').forEach(b => {
      (b as HTMLElement).onclick = async () => {
        const tag = (b as HTMLElement).dataset.ver!;
        ($('overlay-versions') as HTMLElement).classList.add('hidden');
        $('overlay-progress').classList.remove('hidden');
        $('progress-title').textContent = 'Установка zapret ' + tag;
        $('bar-generic').style.width = '0%';
        await tryRun(() => InstallZapretVersion(tag), 'Установлена версия ' + tag);
        $('overlay-progress').classList.add('hidden');
        await refresh();
      };
    });
  };
  $('versions-close').onclick = () => $('overlay-versions').classList.add('hidden');

  // ---------------- старый автозапуск tg-ws
  OldTgwsAutostart().then((p: string) => {
    if (!p) return;
    const el = $('remove-old-autostart');
    el.classList.remove('hidden');
    el.title = p;
  });
  $('remove-old-autostart').onclick = (e) => {
    e.preventDefault();
    tryRun(RemoveOldTgwsAutostart, 'Старый автозапуск убран — вторая иконка больше не появится');
    $('remove-old-autostart').classList.add('hidden');
  };

  // ---------------- настройки / диагностика
  $('btn-settings').onclick = () => $('overlay-settings').classList.remove('hidden');
  $('settings-close').onclick = () => $('overlay-settings').classList.add('hidden');

  // исключение в Windows Defender (qa-2 №1): на первом запуске ставится
  // само, здесь — ручная кнопка на случай переустановки/другого антивируса
  $('btn-defender').onclick = async () => {
    try {
      const msg = await SetupDefenderExclusion();
      toast(msg, true);
    } catch (e: any) {
      toast('Defender: ' + String(e?.message ?? e));
    }
  };

  $('chk-autostart-gui').onchange = (e) =>
    tryRun(() => SetAutostartGui((e.target as HTMLInputElement).checked));

  const minimizedEl = $('chk-start-minimized') as HTMLInputElement;
  minimizedEl.checked = !!status?.startMinimized;
  minimizedEl.onchange = (e) =>
    tryRun(() => SetStartMinimized((e.target as HTMLInputElement).checked));

  $('btn-diagnostics').onclick = async () => {
    $('overlay-settings').classList.add('hidden');
    $('overlay-diag').classList.remove('hidden');
    $('diag-list').innerHTML = '<p class="modal-text">Проверяю…</p>';
    const checks = await RunDiagnostics();
    $('diag-list').innerHTML = checks.map((c: any) => `
      <div class="diag-row">
        <span class="diag-mark ${c.status}"></span>
        <span class="diag-name">${c.name}</span>
        <span class="diag-detail">${c.detail || ''}</span>
        ${c.url ? `<a href="${c.url}" onclick="event.preventDefault()">подробнее</a>` : ''}
      </div>`).join('');
  };
  $('diag-close').onclick = () => $('overlay-diag').classList.add('hidden');
});
