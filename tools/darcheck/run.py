#!/usr/bin/env python3
"""darcheck — типовая проверка darwin-сборки на Windows.

На машине без macOS SDK нельзя собрать .app, но можно провалидировать
типы всех darwin-файлов проекта: делаем vendor-копию зависимостей,
подменяем в ней cgo-файлы (systray, wails-frontend) чистыми заглушками
через go build -overlay и компилируем дерево с CGO_ENABLED=0.

Использование: python tools/darcheck/run.py   (создаёт vendor/ при нужде)
"""
import json
import os
import shutil
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.normpath(os.path.join(HERE, "..", ".."))
STUBS = os.path.join(HERE, "stubs")
VENDOR = os.path.join(ROOT, "vendor")

def fwd(p):  # overlay-пути — только прямые слэши
    return p.replace("\\", "/")

REPLACES = {
    "fyne.io/systray/systray_darwin.go": fwd(os.path.join(STUBS, "systray_darwin.go")),
    "github.com/wailsapp/wails/v2/internal/frontend/desktop/desktop_darwin.go":
        fwd(os.path.join(STUBS, "desktop_darwin.go")),
}

def sh(*args, **kw):
    return subprocess.run(list(args), cwd=ROOT, capture_output=True, text=True, **kw)

def main():
    if not os.path.isdir(VENDOR):
        print("==> go mod vendor (первый запуск, ~минута)…")
        r = sh("go", "mod", "vendor")
        if r.returncode != 0:
            print(r.stderr); sys.exit(1)

    overlay = os.path.join(HERE, "overlay.json")
    replaces = {fwd(os.path.join(VENDOR, k)): v for k, v in REPLACES.items()}
    with open(overlay, "w", encoding="utf-8", newline="\n") as f:
        json.dump({"Replace": replaces}, f, indent=1)

    env = dict(os.environ, GOOS="darwin", GOARCH="arm64", CGO_ENABLED="0")
    targets = [".", "./internal/..."]
    for target in targets:
        r = subprocess.run(["go", "build", "-mod=vendor", "-overlay", overlay, target],
                           cwd=ROOT, env=env, capture_output=True, text=True)
        if r.returncode != 0:
            print(r.stderr)
            print("DARCHECK: FAIL —", target)
            sys.exit(1)
    print("DARCHECK: OK — darwin-файлы типизированы (cgo-фронтенды заменены заглушками)")

if __name__ == "__main__":
    main()
