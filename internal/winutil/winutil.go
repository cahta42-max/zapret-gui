// Package winutil — небольшие обёртки над Win32: проверка прав администратора,
// само-элевация, единственный экземпляр приложения, автозапуск (задача
// планировщика с наивысшими правами — без UAC-запроса при входе).
//go:build windows

package winutil

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// OldTgwsAutostartPath возвращает путь из старой записи автозапуска
// tg-ws-proxy (HKCU Run\TgWsProxy), либо "" если её нет.
func OldTgwsAutostartPath() string {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("TgWsProxy")
	if err != nil {
		return ""
	}
	return strings.Trim(v, `"`)
}

// RemoveOldTgwsAutostart удаляет старую запись автозапуска tg-ws-proxy.
func RemoveOldTgwsAutostart() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.DeleteValue("TgWsProxy")
}

// HiddenCmd создаёт команду, которая не всплывает консольным окном
// (CREATE_NO_WINDOW) — обязательное для GUI-приложений на Windows.
func HiddenCmd(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd
}

// HiddenOutput выполняет команду скрытно и возвращает stdout.
func HiddenOutput(name string, args ...string) ([]byte, error) {
	return HiddenCmd(name, args...).Output()
}

// ------------------------------------------------------------------ Windows Defender

// AddDefenderExclusion добавляет путь в исключения Windows Defender
// (нужны права администратора). Неподписанные WinDivert-драйвер zapret и
// Go-бинарь GUI — частые жертвы ложных срабатываний; исключение снимает
// карантин при первом запуске без ручных блужданий по настройкам.
func AddDefenderExclusion(path string) error {
	out, err := HiddenCmd("powershell", "-NoProfile", "-Command",
		"Add-MpPreference -ExclusionPath '"+path+"'").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DefenderExclusionActive проверяет, числится ли путь в исключениях
// Defender (сравнение без учёта регистра и разделителей).
func DefenderExclusionActive(path string) bool {
	out, err := HiddenOutput("powershell", "-NoProfile", "-Command",
		"(Get-MpPreference).ExclusionPath")
	if err != nil {
		return false
	}
	want := normWinPath(path)
	for _, ln := range strings.Split(string(out), "\n") {
		if normWinPath(ln) == want {
			return true
		}
	}
	return false
}

// normWinPath нормализует путь для сравнения: без пробелов по краям,
// единый разделитель и нижний регистр.
func normWinPath(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "/", "\\"))
}

// IsAdmin сообщает, запущен ли процесс с правами администратора.
func IsAdmin() bool {
	var token windows.Token
	err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token)
	if err != nil {
		return false
	}
	defer token.Close()
	return token.IsElevated()
}

// SelfElevate перезапускает текущий exe с правами администратора (UAC-запрос).
func SelfElevate(extraArgs []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	var args string
	for _, a := range extraArgs {
		args += " " + a
	}
	params, _ := windows.UTF16PtrFromString(args)
	err = windows.ShellExecute(0, verb, file, params, nil, windows.SW_SHOWNORMAL)
	if err != nil {
		return fmt.Errorf("элевация отклонена: %w", err)
	}
	return nil
}

// TryActivateExistingWindow выводит уже запущенное окно на передний план.
// Возвращает true, если окно найдено (значит, приложение реально работает).
func TryActivateExistingWindow(title string) bool {
	user32 := windows.NewLazySystemDLL("user32.dll")
	findW := user32.NewProc("FindWindowW")
	setFg := user32.NewProc("SetForegroundWindow")
	showAsync := user32.NewProc("ShowWindowAsync")
	bringTop := user32.NewProc("BringWindowToTop")
	t, _ := windows.UTF16PtrFromString(title)
	hwnd, _, _ := findW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return false
	}
	showAsync.Call(hwnd, 5) // SW_SHOW — показать скрытое окно
	showAsync.Call(hwnd, 9) // SW_RESTORE — развернуть из минимизации
	bringTop.Call(hwnd)
	setFg.Call(hwnd)
	return true
}

// ---------------------------------------------------------------- single instance

var mutexHandle windows.Handle

var (
	modVersion                  = windows.NewLazySystemDLL("version.dll")
	procGetFileVersionInfoSizeW = modVersion.NewProc("GetFileVersionInfoSizeW")
	procGetFileVersionInfoW     = modVersion.NewProc("GetFileVersionInfoW")
	procVerQueryValueW          = modVersion.NewProc("VerQueryValueW")
)

// ExeFileVersion читает ProductVersion (fallback FileVersion) из
// ресурса версии exe-файла — так TgWsProxy.exe сообщает свою версию.
// Вызовы через syscall.SyscallN; результаты читаем как смещения в buf
// (без uintptr→unsafe.Pointer конверсий — так vet спокоен и GC тоже).
func ExeFileVersion(path string) string {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ""
	}
	size, _, _ := syscall.SyscallN(procGetFileVersionInfoSizeW.Addr(),
		uintptr(unsafe.Pointer(p)), 0)
	if size == 0 {
		return ""
	}
	buf := make([]byte, size)
	ok, _, _ := syscall.SyscallN(procGetFileVersionInfoW.Addr(),
		uintptr(unsafe.Pointer(p)), 0, size, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return ""
	}
	// VerQueryValue возвращает указатели ВНУТРЬ buf — превращаем в offset
	offIn := func(lp uintptr) int {
		return int(lp) - int(reflect.ValueOf(buf).Pointer())
	}
	query := func(key string) string {
		var lp, lplen uintptr
		kp, err := windows.UTF16PtrFromString(key)
		if err != nil {
			return ""
		}
		ok, _, _ := syscall.SyscallN(procVerQueryValueW.Addr(),
			uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(kp)),
			uintptr(unsafe.Pointer(&lp)), uintptr(unsafe.Pointer(&lplen)))
		if ok == 0 || lplen == 0 {
			return ""
		}
		off := offIn(lp)
		if off < 0 || off+int(lplen)*2 > len(buf) {
			return ""
		}
		u16s := make([]uint16, lplen)
		for i := range u16s {
			u16s[i] = binary.LittleEndian.Uint16(buf[off+i*2:])
		}
		s := string(utf16.Decode(u16s))
		if i := strings.IndexByte(s, 0); i >= 0 { // lplen включает NUL-терминатор
			s = s[:i]
		}
		return strings.TrimSpace(s)
	}
	// язык/кодировку берём из VarFileInfo\Translation
	var lp, lplen uintptr
	sub, _ := windows.UTF16PtrFromString(`\VarFileInfo\Translation`)
	ok, _, _ = syscall.SyscallN(procVerQueryValueW.Addr(),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(sub)),
		uintptr(unsafe.Pointer(&lp)), uintptr(unsafe.Pointer(&lplen)))
	if ok == 0 || lplen < 2 {
		return ""
	}
	off := offIn(lp)
	if off < 0 || off+4 > len(buf) {
		return ""
	}
	lang := binary.LittleEndian.Uint32(buf[off:])
	prefix := fmt.Sprintf(`\StringFileInfo\%04x%04x\`, uint16(lang), uint16(lang>>16))
	if v := query(prefix + "ProductVersion"); v != "" {
		return v
	}
	return query(prefix + "FileVersion")
}

// AcquireSingleInstance создаёт именованный мьютекс; false, если приложение
// уже запущено.
func AcquireSingleInstance(name string) bool {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return true
	}
	handle, err := windows.CreateMutex(nil, false, ptr)
	// при успешном создании существующего мьютекса err == ERROR_ALREADY_EXISTS
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(handle)
		return false
	}
	if handle != 0 {
		mutexHandle = handle
	}
	return true // прочие ошибки не блокируем запуск
}

// SingleInstanceAlive сообщает, держит ли какой-то процесс именованный
// мьютекс, при этом сам его НЕ создаёт и не удерживает (OpenMutex, а не
// CreateMutex): используется супервизором --watchdog-respawn, который
// опрашивает мьютекс раз в полсекунды — создание «пробного» мьютекса
// могло бы гониться со стартующим экземпляром и съедать его запуск.
func SingleInstanceAlive(name string) bool {
	ptr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, ptr)
	if err != nil {
		return false // мьютекса нет — экземпляр не запущен
	}
	windows.CloseHandle(handle)
	return true
}

// ---------------------------------------------------------------- autostart

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutostart включает/выключает автозапуск приложения.
//
// Автозапуск — задача планировщика с триггером «при входе» и уровнем
// Highest: приложение стартует уже с правами администратора, и UAC-запрос
// при каждом входе в систему не появляется. Создание такой задачи требует
// прав администратора — GUI к этому моменту сам элевирован.
//
// Регистрируем через PowerShell Register-ScheduledTask (API планировщика
// 2.0, объектные параметры). Прежний способ — schtasks /Create
// /SC ONLOGON — молча ставил враждебные для «вечного» GUI дефолты:
// DisallowStartIfOnBatteries=true и StopIfGoingOnBatteries=true (на
// ноутбуке задача НЕ стартует от батареи и УБИВАЕТСЯ при выдёргивании
// шнура; инцидент 25.08: watchdog перезапускался «через задачу», а /Run
// получал отказ 0x800710E0 — 65 минут мёртвого приложения) и
// ExecutionTimeLimit=72ч (планировщик убил бы процесс через 3 суток).
// Вариант со schtasks /XML на машине юзера отвергается ошибкой
// «неожидаемое пространство имён» (проверено 25.08 утилитой
// tools/testtask), Register-ScheduledTask — работает. Фолбэк — прежний
// /SC ONLOGON (кривой, но живой).
//
// Старую запись в HKCU Run (если осталась) удаляем: через неё Windows
// запускает exe НЕ-админом, что и вызывало UAC-запрос при входе.
func SetAutostart(appName string, enable bool) error {
	if enable {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := createAutostartTask(appName, exe); err != nil {
			// фолбэк на прежний способ (не должен случаться, но автозапуск
			// лучше кривой, чем никакого)
			out, err2 := HiddenCmd("schtasks", "/Create", "/F",
				"/TN", appName,
				"/TR", `"`+exe+`"`,
				"/SC", "ONLOGON",
				"/RL", "HIGHEST",
			).CombinedOutput()
			if err2 != nil {
				return fmt.Errorf("register-scheduledtask (%v): schtasks: %v: %s", err, err2, strings.TrimSpace(string(out)))
			}
		}
		removeRunKeyAutostart(appName) // миграция со старого способа (HKCU Run)
		return nil
	}
	removeRunKeyAutostart(appName)
	_, _ = HiddenCmd("schtasks", "/Delete", "/F", "/TN", appName).CombinedOutput()
	return nil
}

// createAutostartTask (пере)создаёт задачу автозапуска через PowerShell:
// старт на батарее и от батареи разрешён, лимита времени выполнения нет
// (PT0S), StartWhenAvailable, IgnoreNew. Principal — текущий пользователь
// (Register-ScheduledTask принимает и SID), InteractiveToken + Highest —
// без UAC при входе.
func createAutostartTask(appName, exe string) error {
	user := currentUserSid()
	if user == "" {
		user = os.Getenv("USERDOMAIN") + `\` + os.Getenv("USERNAME")
	}
	psScript := `$ErrorActionPreference='Stop'
try {
  $a = New-ScheduledTaskAction -Execute '` + exe + `'
  $t = New-ScheduledTaskTrigger -AtLogOn
  $p = New-ScheduledTaskPrincipal -UserId '` + user + `' -LogonType Interactive -RunLevel Highest
  $s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable -MultipleInstances IgnoreNew
  Register-ScheduledTask -TaskName '` + appName + `' -Action $a -Trigger $t -Principal $p -Settings $s -Force | Out-Null
  Write-Output 'OK'
} catch { Write-Output ('FAIL: ' + $_.Exception.Message); exit 1 }`
	out, err := HiddenCmd("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psScript).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "OK") {
		return fmt.Errorf("powershell: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// currentUserSid — SID текущего пользователя (для Principal задачи).
func currentUserSid() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
}

// removeRunKeyAutostart удаляет старую запись автозапуска в HKCU Run
// (отсутствие записи — не ошибка).
func removeRunKeyAutostart(appName string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	_ = k.DeleteValue(appName)
}

// AutostartEnabled проверяет, включён ли автозапуск: задача планировщика
// или ещё не мигрировавшая запись HKCU Run.
func AutostartEnabled(appName string) bool {
	if HiddenCmd("schtasks", "/Query", "/TN", appName).Run() == nil {
		return true
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(appName)
	return err == nil
}

// TryLaunchElevatedViaTask запускает приложение через задачу планировщика
// с RunLevel Highest: новый экземпляр стартует уже с правами администратора
// и БЕЗ UAC-запроса. Возвращает false, если задачи нет или она указывает
// на другой путь exe (тогда вызывающий код может показать UAC через
// SelfElevate — админ-запуск затем пересоздаст задачу по актуальному пути).
func TryLaunchElevatedViaTask(appName string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	xml, err := HiddenCmd("schtasks", "/Query", "/TN", appName, "/XML").Output()
	if err != nil || !asciiContains(xml, exe) {
		return false // UTF-16 вывод schtasks разбирает asciiContains
	}
	out, err := HiddenCmd("schtasks", "/Run", "/TN", appName).CombinedOutput()
	if err != nil {
		_ = strings.TrimSpace(string(out))
		return false
	}
	return true
}

// asciiContains ищет ASCII-подстроку s в буфере b независимо от кодировки:
// в сыром виде или в UTF-16LE (schtasks /XML выводит UTF-16, из-за чего
// обычный bytes.Contains по ASCII-пути всегда промахивался → лишний UAC).
func asciiContains(b []byte, s string) bool {
	if strings.Contains(string(b), s) {
		return true
	}
	// UTF-16LE: ASCII-символы чередуются с нулевым байтом
	var sb strings.Builder
	for i := 0; i+1 < len(b); i += 2 {
		sb.WriteByte(b[i])
	}
	return strings.Contains(sb.String(), s)
}

// ---------------------------------------------------------------- misc

// OpenURL открывает ссылку/файл системным обработчиком.
func OpenURL(url string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(url)
	_ = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

// OpenFolder открывает папку в Проводнике.
func OpenFolder(path string) { OpenURL(path) }

// RunBatVisible открывает bat-файл в видимой консоли (сохраняем терминальный
// функционал оригинала).
func RunBatVisible(dir, bat string) error {
	cmd := syscall.StringToUTF16Ptr(`/c start "" /D "` + dir + `" "` + bat + `"`)
	return windows.ShellExecute(0, nil, syscall.StringToUTF16Ptr("cmd.exe"), cmd, nil, windows.SW_SHOWNORMAL)
}
