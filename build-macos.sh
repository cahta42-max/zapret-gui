#!/bin/bash
# Сборка Zapret GUI для macOS: universal .app + .dmg (Intel + Apple Silicon).
# Запускать на Mac из корня проекта:  bash build-macos.sh
set -euo pipefail

cd "$(dirname "$0")"

echo "==> Проверяю инструменты…"
if ! xcode-select -p >/dev/null 2>&1; then
  echo "ОШИБКА: нет Xcode Command Line Tools. Установите: xcode-select --install"
  exit 1
fi
command -v go >/dev/null || { echo "ОШИБКА: нет Go (https://go.dev/dl/)"; exit 1; }
command -v node >/dev/null || { echo "ОШИБКА: нет Node.js (https://nodejs.org/)"; exit 1; }
export PATH="$PATH:$(go env GOPATH)/bin"
if ! command -v wails >/dev/null; then
  echo "==> Ставлю Wails CLI (2-3 минуты)…"
  go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
fi

echo "==> Собираю universal-бинарник (arm64 + amd64)…"
export MACOSX_DEPLOYMENT_TARGET=11.0
if ! wails build -platform darwin/universal -clean; then
  ARCH="$(uname -m)"
  echo "!! universal не собрался — собираю под текущую архитектуру ($ARCH)"
  wails build -platform "darwin/$([ "$ARCH" = arm64 ] && echo arm64 || echo amd64)" -clean
fi

# бандл может называться по имени проекта (zapret_gui.app) — берём любой .app
APP=$(find build/bin -maxdepth 1 -name '*.app' | head -1)
test -n "$APP" -a -d "$APP" || { echo "ОШИБКА: .app не собрался"; exit 1; }

# генератор биндингов запускает наш main (singleton-lock!) и иногда
# зависает — убиваем хвосты сборки, чтобы не блокировать запуск приложения
pkill -u "$(id -u)" -f wailsbindings >/dev/null 2>&1 || true

echo "==> Подписываю (ad-hoc)…"
codesign --force --deep --sign - "$APP"

echo "==> Собираю DMG…"
VERSION=$(plutil -extract CFBundleShortVersionString raw "$APP/Contents/Info.plist" 2>/dev/null || echo dev)
STAGE=$(mktemp -d)
cp -R "$APP" "$STAGE/ZapretGui.app"
ln -s /Applications "$STAGE/Applications"
DMG="dist/ZapretGui-macOS-universal-${VERSION}.dmg"
mkdir -p dist
rm -f "$DMG"
hdiutil create -volname "Zapret GUI" -srcfolder "$STAGE" -fs HFS+ -format UDZO -ov "$DMG" >/dev/null
rm -rf "$STAGE"
codesign --force --sign - "$DMG"

echo ""
echo "ГОТОВО: $DMG"
echo "Первый запуск на другом Mac: правый клик по ZapretGui.app → «Открыть»"
