# Zapret GUI для macOS — сборка и использование

Порт Windows-версии на macOS. На macOS **zapret (winws/WinDivert) не
существует** — движком DPI-обхода с **v0.2.2-mac.1** служит **utunws** из
официального macOS-форка Flowseal
([zapret-mac-discord-youtube](https://github.com/Flowseal/zapret-mac-discord-youtube)):
C-порт nfqws с системным перехватом (pf anchor + туннель utun50), тот же
класс пакетного desync, что winws на Windows, и те же названия стратегий
`general (ALT…)` 1-в-1. tg-ws-proxy — официальный macOS-билд (universal).
История перехода: SpoofDPI (v0.2.1-mac.1) не прошёл DPI юзера —
[macos-engine-utunws.md](./macos-engine-utunws.md); анализ порта —
[macos-analysis.md](./macos-analysis.md). Версионность платформ
независимая — см. [VERSIONING.md](./VERSIONING.md): Windows `0.2.x`,
macOS `0.2.x-mac.N`.

## Архитектура (кратко)

```
ZapretGui.app (Wails, universal)
 ├─ движок utunws (ZapretMac, universal): устанавливается в
 │    /Library/Application Support/ZapretMac + LaunchDaemon (root):
 │    pf anchor route-to utun50, raw-сокеты, sysctl keepinit=7000
 │    · запуск/остановка/смена стратегии — их restart.sh/stop.sh
 │      через osascript (один запрос пароля на действие)
 │    · полный тест стратегий — ОДИН запрос пароля на весь прогон
 │      (root-цикл + URL-тесты GUI, синхронизация маркер-файлами)
 ├─ «Стратегии» = strategies.tsv дистрибутива: general, ALT…12,
 │    FAKE TLS AUTO ×4, SIMPLE FAKE ×3, EXP — названия 1-в-1 с Windows
 ├─ IPSet none/any/loaded — файл ipset-mode; списки юзера *-user.txt
 │    в ~/Library/Application Support/ZapretMac/lists/
 └─ tg-ws-proxy: официальный TgWsProxy_macos_universal.dmg
      └─ монтируем, копируем .app, запускаем бинарь с --portable
```

Данные GUI: `~/Library/Application Support/ZapretGui/` (tools/, config.json,
log.txt). Данные движка: `~/Library/Application Support/ZapretMac/`
(selected-strategy, ipset-mode, lists/). Автозапуск GUI: LaunchAgent
`~/Library/LaunchAgents/com.buzmakov.ZapretGui.plist`; автозапуск движка —
LaunchDaemon (сам стартует при загрузке системы).

**Требования: macOS 14+** (требование ZapretMac), **внешний DNS обязателен**
(8.8.8.8/1.1.1.1 или DoH-профиль [paulmillr/encrypted-dns](https://github.com/paulmillr/encrypted-dns)):
провайдерский резолвер отравляется, и обход бессилен — диагностика в GUI
предупредит. VPN-туннели всего трафика — выключить.

## Сборка (на Mac)

Требуются: Xcode Command Line Tools, Go 1.25+, Node.js.

```bash
bash build-macos.sh
```

Скрипт соберёт `dist/ZapretGui-macOS-universal-<версия>.dmg`
(Intel + Apple Silicon, ad-hoc подпись). Для разработки: `wails dev`.

## Первый запуск (для друзей)

1. Открыть dmg, перетащить ZapretGui.app в «Программы».
2. **Первый запуск: правый клик по приложению → «Открыть» → «Открыть»**
   (или Системные настройки → Конфиденциальность и безопасность → «Все равно
   открыть»). Это нужно один раз — приложение без нотаризации Apple
   (Developer ID платный; админ-подпись ad-hoc).
3. Программа сама скачает ZapretMac (utunws) и tg-ws-proxy с GitHub,
   поставит движок (один запрос пароля администратора — LaunchDaemon) и
   включит обход.
4. Миграция со старой версии (spoofdpi) — автоматическая: системный прокси
   выключается, старый процесс глушится, каталог удаляется.

## Отличия от Windows-версии

| Windows | macOS |
|---|---|
| zapret (winws + WinDivert), драйвер | utunws (C-порт nfqws): pf + utun50, LaunchDaemon |
| Служба Windows (до входа) | LaunchDaemon движка (до входа) + LaunchAgent GUI |
| Права администратора обязательны | пароль — на установку/запуск/остановку/тест (osascript) |
| general*.bat — стратегии | strategies.tsv: те же названия general (ALT…12) |
| Тест: `test zapret.ps1` (36 хостов) | тест: перебор стратегий по URL (один пароль) |
| Game Filter | нет (в ZapretMac не переносится, скрыт) |
| IPSet none/any/loaded | есть — ipset-mode (1-в-1 с Windows) |
| service.bat / general.bat в терминале | нет (скрыто в UI) |
| TgWsProxy.exe, автозапуск через их конфиг | TgWsProxy.app, автозапуск поднимает GUI |

## Известные особенности

- Выход из GUI **не останавливает движок** — LaunchDaemon продолжает
  работать (аналог службы Windows). Остановить — кнопкой «Остановить».
- После «Остановить» движок не поднимется при перезагрузке сам (stop.sh
  отключает автозапуск демона) — запустите из GUI.
- Смена сети: run.sh сам следит за шлюзом и перезапускается (KeepAlive);
  наш старый proxyWatchLoop больше не нужен.
- Спойлер v0.2.1 (историческое): прокси-режим SpoofDPI покрывал только
  приложения, уважающие системный прокси, и слетал при смене сети.
