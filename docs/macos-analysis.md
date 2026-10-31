# macOS-порт Zapret GUI — анализ реализуемости

Дата: 2026-08-20. Версия анализа: 1.0.
Вывод: **реализуемо**, но не «пересборка», а портирование с заменой движка:
Windows-ядро (WinDivert/winws) на macOS не существует.

---

## 1. Что мы имеем (инвентаризация Windows-зависимостей)

| Компонент | Файл(ы) | Зависимость от Windows | macOS-эквивалент |
|---|---|---|---|
| GUI-каркас | main.go, frontend/ | Wails v2 + WKWebView — кросс-платформенный | тот же код, сборка `darwin/universal` |
| Трей | main.go (fyne.io/systray) | Windows: `Run()` в горутине | `RunWithExternalLoop()` — Wails владеет NSApp runloop |
| Права админа | winutil.IsAdmin/SelfElevate, wails.exe.manifest | UAC, реестр, schtasks | НЕ НУЖНЫ в прокси-режиме (scutil); опц. osascript-запрос |
| Единственный экземпляр | winutil.AcquireSingleInstance | Win32-мьютекс | flock на файл в ~/Library/Caches |
| Автозапуск GUI | winutil.SetAutostart (schtasks ONLOGON, RL HIGHEST) | планировщик задач | LaunchAgent (~/Library/LaunchAgents, без root) |
| Движок DPI | internal/zapret: winws.exe + WinDivert64.sys + general*.bat | **Windows-only** (драйвер ядра) | **замена: SpoofDPI** (см. §3) |
| Служба zapret | manager.go: sc create/start/stop, reg query | SCM + реестр | LaunchDaemon/LaunchAgent |
| Стратегии | strategy.go: парсинг general*.bat → argv winws | bat-файлы дистрибутива | профили флагов SpoofDPI (наши «стратегии») |
| Полный тест | terminaltest.go: `test zapret.ps1` (DPI-чекеры) | PowerShell-скрипт дистрибутива | собственный перебор профилей + TestUrl (strattest.go — кросс) |
| Game Filter / IPSet / Check Updates | manager.go (файлы дистрибутива) | концепты zapret | не переносятся; аналог — правила SpoofDPI (rules) |
| Переключатели процессов | tasklist/taskkill | taskmgr | pgrep/pkill |
| Диагностика | diag.go: BFE, netsh timestamps, реестр DoH, hosts, WinDivert, Adguard | Win32 | новая: scutil --proxy/--dns, /etc/hosts, utun-конфликты, quarantine |
| Скачивание релизов | release.go (GitHub API) | кросс | тот же + выбор ассета по GOOS/GOARCH |
| tg-ws-proxy | tgws.go: TgWsProxy.exe --portable | exe | **официальный macOS-билд есть**: TgWsProxy_macos_universal.dmg |
| Каталоги данных | config.go: %LOCALAPPDATA%\ZapretGui | env | ~/Library/Application Support/ZapretGui (portable-режим тот же) |
| Открытие папок/bat | winutil.OpenFolder / RunBatVisible | ShellExecute | `open`; bat-менеджер не нужен |
| Версия из ресурсов exe | winutil.ExeFileVersion (version.dll) | Win-ресурсы | `--version` флаг бинарей |

Оценка: **~30% кода платформенно-нейтрально** (release, strattest, config-каркас, весь frontend),
**~40% требует обёртки платформой**, **~30% Windows-only по смыслу** (служба SCM, bat-стратегии,
терминальный тест, UAC-логика) — на macOS заменяется другими реализациями.

## 2. Чем НЕ является macOS-мир (чтобы не тратить время)

- **Flowseal/zapret-discord-youtube** — только Windows: релиз содержит winws.exe,
  WinDivert*.sys, *.bat. Проверено по последнему релизу 1.10.1 (только rar/tar/zip с exe).
- **bol-van/zapret** — Linux (NFQUEUE/iptables). macOS-порта нет и не планируется.
- **GoodbyeDPI** — Windows (WinDivert).
- Packet-level системный перехват на macOS (pf divert / Network Extension) —
  несовместим с моделью zapret; Network Extension требует entitlements и App Store-процесс.

## 3. Движок для macOS: SpoofDPI (рекомендация)

**github.com/xvzc/SpoofDPI** — Go, ~5k звёзд, активен (обновлён 2026-08-19).

Почему подходит:
- **Официальные darwin-сборки** в релизах: `spoofdpi_X_darwin_arm64.tar.gz`,
  `spoofdpi_X_darwin_x86_64.tar.gz` (кстати есть и windows — единый код скачивания).
- Режимы: **http** (CONNECT-прокси), socks5, **tun** (экспериментальный, utun — системный).
- Методы обхода: fake packets, disorder, split chunk/sni/random, DNS over HTTPS,
  правила по доменам (rules) — функциональный аналог «стратегий» zapret.
- Имеет `auto-configure-network`: сам ставит/снимает системный прокси
  (networksetup + PAC, с rollback-файлом состояния) — но требует root на macOS,
  поэтому в v1 управляем прокси сами через `scutil --proxy` (без прав администратора!).
- Идеологически тот же класс решения: RU-DPI (YouTube/Discord) — его основная аудитория.

Альтернативы, отклонённые:
- **hufrea/byedpi** (C, SOCKS5, «стратегии» -s…): официальных darwin-ассетов нет —
  значит свой CI-билд чужого C-проекта; меньше гарантий. Отложено как вариант №2.
- **SpoofDPI TUN-режим**: системный (прокси не нужен в настройках), но экспериментальный
  и требует root. Кандидат на v2.
- **romanvht/ByeByeDPI, ByeDPIAndroid, Podkop** — Windows/Android/OpenWRT, не macOS.

## 4. Модель работы на macOS (v1)

```
ZapretGui.app (Wails, universal)
 ├─ spoofdpi (darwin, http-режим, 127.0.0.1:8080)
 │    └─ при запуске: scutil --proxy → HTTP/HTTPS proxy = 127.0.0.1:8080
 │       при остановке/выходе: scutil --proxy → сброс
 │       (без root; действует на Safari/Chrome и все приложения пользователя;
 │        после перезагрузки настройки сами не восстанавливаются — «безопасно по умолчанию»)
 ├─ «Стратегии» = профили SpoofDPI: default / fake-aggressive / split-sni / disorder / dns-doh…
 │    └─ «Полный тест» = собственный перебор профилей с проверкой URL (strattest.TestUrl уже кросс)
 └─ tg-ws-proxy: скачиваем официальный TgWsProxy_macos_universal.dmg
      └─ hdiutil attach → копируем .app в tools/ → detach
         запускаем бинарь .app с --portable (портативный TgWsProxy_data — как на Windows)
```

Автозапуск: LaunchAgent пользователя (`~/Library/LaunchAgents/com.zapretgui.app.plist`)
— без root, стартует при входе. Служба-до-входа (LaunchDaemon) — только если движку нужен
root (TUN-режим v2).

Права администратора: в v1 не требуются вовсе (scutil). Опционально в v2:
- networksetup (persistent системный прокси) — разовый osascript-запрос пароля;
- TUN-режим SpoofDPI — root.

## 5. Что переносится один-в-один

- Весь frontend (HTML/TS/CSS) + биндинги Wails (адаптация: скрыть Windows-секции).
- strattest.TestUrl (проверка URL) — платформенно-нейтральный.
- release.go — GitHub API (добавить выбор ассета по GOOS/GOARCH + распаковку tar.gz).
- tgws.go — на 80%: конфиг/секрет/порт/Link()/PortOpen() те же;
  отличается способ установки (dmg) и путь бинаря.
- config.go — та же схема, другой DataDir.
- Логика «полного теста» — тот же UX, другой итератор (профили вместо bat).

## 6. Сборка и дистрибуция (.dmg)

- `wails build -platform darwin/universal` → ZapretGui.app (Intel + Apple Silicon).
  **Кросс-сборка из Windows невозможна** (нужны macOS SDK, cgo/objc для WKWebView и трея).
- Варианты: (а) есть Mac у юзера/друзей; (б) **GitHub Actions** (macos-latest) —
  в репо нет origin, надо создать; (в) rent mac в облаке. Рекомендуем (б).
- Иконка appicon.png уже есть (split-Z), build/darwin/Info.plist в проекте есть.
- codesign ad-hoc (обязателен на Apple Silicon), dmg через hdiutil или create-dmg.
- **Notarization**: нужен Apple Developer ID ($99/год). Без него друзья открывают
  первый раз через «правый клик → Открыть» (или Системные настройки → Всё равно открыть).
  Для «поделиться с друзьями» приемлемо; заметим в README.

## 7. Риски и открытые вопросы

| # | Риск | Вероятность | Митигация |
|---|---|---|---|
| R1 | systray + Wails на darwin (два NSApp) | средняя | `RunWithExternalLoop` — стандартный паттерн; тест в CI невозможен, проверить на реальном Mac; fallback: без трея в v1 (окно + LaunchAgent) |
| R2 | scutil --proxy подхватывается не всеми приложениями | низкая | Safari/Chrome/Firefox — да; проверить; fallback: networksetup через osascript (разовый пароль) |
| R3 | SpoofDPI не берёт конкретную сеть юзера | средняя | у нас есть «полный тест» с перебором профилей — тот же UX, что на Windows |
| R4 | нет macOS для рантайм-тестов | высокая | собрать в GH Actions; рантайм-чек на Mac юзера/друзей по чек-листу |
| R5 | quarantine/Gatekeeper на скачанных движках | низкая | мы качаем своим HTTP-клиентом — xattr не ставится; отслеживать в диагностике |
| R6 |SpoofDPI ломает совместимость флагов в обновлениях | средняя | пиновать минорную версию, обновлять вручную |
| R7 | спящий режим/смена сети сбрасывает прокси | средняя | периодический watcher состояния + авто-восстановление; диагностика |

## 8. План работ (после обсуждения)

1. **Платформенный слой**: internal/platform (интерфейс) + перенос winutil за виндой;
   main.go: build-tags darwin/windows (элевация, трей-режим, единый инстанс).
2. **Движок SpoofDPI** (darwin): скачивание по GOARCH, профили-«стратегии»,
   процесс-менеджмент (pgrep/pkill), scutil-proxy менеджер (set/unset/watcher).
3. **Полный тест**: перебор профилей через TestUrl, события — те же.
4. **tg-ws macOS**: dmg→app→запуск --portable, конфиг, Link().
5. **Диагностика macOS**: прокси, DNS, hosts, utun/VPN, quarantine, версии.
6. **Frontend**: платформенные флаги в Status, скрытие Windows-секций, термины.
7. **LaunchAgent**: автозапуск GUI и движка.
8. **CI**: workflow macOS → wails universal → codesign ad-hoc → dmg → artifact.
9. **README-macOS** + чек-лист рантайм-тестов для юзера.

Объём: ~8-9 плотных рабочих сессий (по меркам прошлых итераций проекта).
