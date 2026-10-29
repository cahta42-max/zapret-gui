# macOS-движок v2: utunws (ZapretMac) — смена SpoofDPI

Дата: 2026-08-21. Статус: **решение принято**, реализация в 0.2.2-mac.х / 0.3.
Причина: на сборке 0.2.1-mac.1 SpoofDPI не открыл YouTube у юзера.

## Контекст: почему SpoofDPI не справился

SpoofDPI — локальный HTTP-прокси + системный прокси через scutil. Ограничения
зафиксированы в README-macos.md и подтвердились на практике:

1. Прокси-режим покрывает только приложения, уважающие системный прокси.
2. scutil-настройка живёт до смены сети; после перезагрузки не поднимается.
3. Главное: SpoofDPI — самостоятельный набор трюков, ≠ стратегии zapret.
   Сети провайдеров, где работает zapret-стек (fake/multisplit/seqovl по
   точным payload'ам), SpoofDPI не всегда проходит — что и произошло.

## Источники (по заданию юзера, 2026-08-21)

- Обсуждение «Умоляю: запрет для MacOS» —
  https://github.com/Flowseal/zapret-discord-youtube/discussions/10741
  (запрос macOS-версии, в комментариях люди указывают на решения ниже).
- **Flowseal/zapret-mac-discord-youtube** — официальный macOS-форк от автора
  win-сборки: https://github.com/Flowseal/zapret-mac-discord-youtube
  Релиз 1.1.2 (2026-08-14), `ZapretMac-macOS-universal.zip` (~0.4 МБ),
  репо активен (обновление 2026-08-21), 104 звезды.
- **nikitaSobolev2/zapret2** — приложение поверх того же движка:
  https://github.com/nikitaSobolev2/zapret2 (v2.4.0, dmg ~94 МБ, Java/Gradle,
  Homebrew cask). Движок и стратегии — адаптация ZapretMac. Люди в
  комментариях подтверждают работу. Как приложение нам не подходит (наш GUI
  уже есть), как подтверждение работоспособности движка — да.

## ZapretMac: что это (изучено по репо, master)

- Движок: **`utunws`** — C-порт `nfqws` из bol-van/zapret (`nfq/` в репо,
  включая `macos_utun.c`). Пакетный DPI-desync — тот же класс, что winws
  на Windows. НЕ прокси: перехват системный.
- Механика: LaunchDaemon → `run.sh` (root) → pf anchor `com.apple/zapret-macos`
  заворачивает трафик в `utun50`, движок режет/подменяет пакеты, отправка
  через raw-socket с MAC шлюза (arp/ndp). sysctl `net.inet.tcp.keepinit=7000`
  (аналог TCP-твика из service.bat).
- Стратегии: 22 шт., **названия 1-в-1 с Windows-сборкой** (general, ALT1–12,
  FAKE TLS AUTO ×4, SIMPLE FAKE ×3, EXP) — `strategies/*.conf.in` (argv
  nfqws: --dpi-desync=fake/multisplit, split-seqovl, fake-пакеты .bin).
  GameFilter вырезан (на mac не переносится).
- Списки: те же файлы, что в Windows (`list-general.txt`, `list-google.txt`,
  `ipset-all.txt`, `*-user.txt`…) — пользовательские правки переносятся.
- IPSet-режимы: none / loaded (ipset-all) / any — как в нашем win-GUI.
- Управление (то, что будет дёргать наш GUI):
  - `install.sh <src> <data_root>` — установка в
    `/Library/Application Support/ZapretMac` + LaunchDaemon (sudo, разово);
  - `~/Library/Application Support/ZapretMac/selected-strategy` — файл с
    именем стратегии;
  - `~/…/ipset-mode` — файл none/loaded/any;
  - `restart.sh` — применение смены стратегии/списков (sudo);
  - `stop.sh` — остановка (sudo); watchdog.sh — следит за демоном;
  - `test-strategies.sh` — их полный тест (у нас свой — strattest по URL).
- Требования: **macOS 14+**, universal (arm64+x86_64), **внешний DNS обязателен**
  (8.8.8.8/1.1.1.1 или DoH-профиль paulmillr/encrypted-dns) — иначе резолв
  отравляется провайдером и обход бессилен. VPN-туннели всего трафика — выкл.

## Решение для Zapret GUI (v0.2.2-mac.x / 0.3)

Смести SpoofDPI → **utunws из ZapretMac**:

1. **Установка движка**: качаем релиз ZapretMac (universal zip), распаковка,
   `install.sh` через osascript (один запрос пароля — как networksetup
   сейчас). Наш GUI остаётся GUI: их Swift-трей-меню не ставим.
2. **Стратегии**: наш `strategy-select` пишет `selected-strategy` (id вида
   `general-alt9`) + `restart.sh`. Отображаемые имена — из `strategies.tsv`
   (general (ALT9) …) — UX совпадает с Windows-карточкой 1-в-1.
3. **IPSet**: сегменты none/any/loaded → `ipset-mode` + restart. Списки
   юзера (`*-user.txt`) редактируются как на win.
4. **Тест**: наш strattest (перебор стратегий + TestUrl по 36 хостов) —
   то же, что на Windows; их `test-strategies.sh` не обязателен.
5. **Диагностика**: проверка DNS (scutil --dns: провайдерский резолвер →
   предупреждение с инструкцией DoH-профиля), utun50 поднят, LaunchDaemon
   жив, pf anchor чист при остановке, keepinit восстановлен.
6. **Права**: движку нужен root (LaunchDaemon). Один osascript-пароль на
   установку/смену стратегии (restart.sh). Это ОТЛИЧАЕТСЯ от v1 (scutil без
   root) — но v1 не работал; рабочий обход важнее. Текущий SpoofDPI-код
   (engine/, macutil/proxy.go) — удалить/закомментировать в 0.3, до того —
   живёт рядом для отката.
7. **tg-ws-proxy** — не меняется (без root, как сейчас).

Открытые вопросы (проверить на Mac юзера при развёртывании агента):

- Версия macOS юзера ≥ 14? (ZapretMac требует 14+)
- Поведение при смене сети (watchdog перезапускает pf-правила?) — наш
  proxyWatchLoop заменяется мониторингом LaunchDaemon + utun50.
- Сосуществование со старым SpoofDPI-прокси (снять системный прокси при
  миграции!).

## Миграция со SpoofDPI (для следующей сборки dmg)

1. При апгрейде: снять системный прокси (scutil) и остановить spoofdpi.
2. Скачать ZapretMac-релиз, install.sh (пароль), записать текущую стратегию
   юзера в selected-strategy (маппинг имён профилей → general-* id).
3. Проверка: `ifconfig utun50` + тест YouTube.
4. Fallback: если utunws не завёлся — вернуть SpoofDPI нельзя молча:
   сообщить юзеру и предложить логи engine.log.

## Чек-лист релиза dmg (впитывает qa-2 и это решение)

- [ ] AppVersion = `0.2.2-mac.1` (VERSIONING.md!)
- [ ] qa-2 фиксы уже в darwin-коде: ЛКМ по трею toggle, чекбоксы по
      квадратику, автозапуск движка только после выбора стратегии
- [ ] движок utunws + install/restart/stop через osascript
- [ ] тест стратегий против utunws; сегменты IPSet mac-совместимые
- [ ] диагностика DNS/utun50/LaunchDaemon; предупреждение про DoH
- [ ] README-macos.md обновлён (таблица отличий, требования macOS 14+,
      DNS); VERSIONING.md — журнал версий
- [ ] dmg: universal, ad-hoc подпись, имя `ZapretGui-macOS-universal-0.2.2-mac.1.dmg`
