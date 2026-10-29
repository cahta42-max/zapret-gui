//go:build windows

// Утилита диагностики XML-регистрации задачи автозапуска.
// Запускается elevated: пишет результат каждого шага в
// %LOCALAPPDATA%\ZapretGui\autostart-debug.txt и в stdout.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"

	"zapret_gui/internal/winutil"
)

func log(f *os.File, format string, args ...interface{}) {
	line := fmt.Sprintf(format+"\n", args...)
	fmt.Print(line)
	if f != nil {
		f.WriteString(line)
	}
}

func main() {
	dbgPath := filepath.Join(os.Getenv("LOCALAPPDATA"), "ZapretGui", "autostart-debug.txt")
	os.MkdirAll(filepath.Dir(dbgPath), 0o755)
	f, err := os.OpenFile(dbgPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		f = nil
	}
	defer f.Close()

	admin := winutil.IsAdmin()
	log(f, "=== testtask %s admin=%v ===", "run", admin)

	// 1) SID
	sid := currentUserSid()
	log(f, "sid=%q", sid)

	// 2) XML — точь-в-точь как в winutil.createAutostartTask
	exe := `C:\work\AI\pi\Zapret_gui\build\bin\ZapretGui.exe`
	xml := fmt.Sprintf(taskXmlTemplate, sid, xmlEscape(exe))
	log(f, "xml-len=%d", len(xml))

	xmlPath := filepath.Join(os.TempDir(), "ZapretGui-autostart-test.xml")
	if err := writeUtf16File(xmlPath, xml); err != nil {
		log(f, "writeUtf16File: ERR %v", err)
		return
	}
	log(f, "xml written: %s", xmlPath)
	st, _ := os.Stat(xmlPath)
	log(f, "xml size=%d", st.Size())

	// 3) регистрация под тестовым именем (ZapretGui не трогаем!)
	out, err := winutil.HiddenCmd("schtasks", "/Create", "/F",
		"/TN", "ZapretGuiXmlGoTest", "/XML", xmlPath).CombinedOutput()
	log(f, "schtasks /XML: err=%v out=%q", err, strings.TrimSpace(string(out)))
	os.Remove(xmlPath)

	// 4) второй путь: экспорт текущей задачи + точечные правки batteries/TimeLimit
	exp, err := winutil.HiddenCmd("schtasks", "/Query", "/TN", "ZapretGui", "/XML").Output()
	if err != nil {
		log(f, "query export: ERR %v", err)
		return
	}
	fixed := strings.ReplaceAll(string(utf16ToUtf8(exp)),
		"<DisallowStartIfOnBatteries>true</DisallowStartIfOnBatteries>",
		"<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>")
	fixed = strings.ReplaceAll(fixed,
		"<StopIfGoingOnBatteries>true</StopIfGoingOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>")
	fixed = strings.ReplaceAll(fixed,
		"<ExecutionTimeLimit>PT72H</ExecutionTimeLimit>",
		"<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>")
	log(f, "fixed-export contains PT0S: %v", strings.Contains(fixed, "PT0S"))
	expPath := filepath.Join(os.TempDir(), "ZapretGui-export-fixed.xml")
	if err := writeUtf16File(expPath, fixed); err != nil {
		log(f, "write export: ERR %v", err)
		return
	}
	out2, err2 := winutil.HiddenCmd("schtasks", "/Create", "/F",
		"/TN", "ZapretGuiXmlGoTest2", "/XML", expPath).CombinedOutput()
	log(f, "schtasks /XML (export-fixed): err=%v out=%q", err2, strings.TrimSpace(string(out2)))
	os.Remove(expPath)

	// 5) путь без XML вообще: Register-ScheduledTask с параметрами
	psScript := `$ErrorActionPreference='Stop'
try {
  $a = New-ScheduledTaskAction -Execute 'C:\Windows\System32\notepad.exe'
  $t = New-ScheduledTaskTrigger -AtLogOn
  $p = New-ScheduledTaskPrincipal -UserId 'DESKTOP-5UPJVVS\buzmakov' -LogonType Interactive -RunLevel Highest
  $s = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable -MultipleInstances IgnoreNew
  Register-ScheduledTask -TaskName 'ZapretGuiPsTest' -Action $a -Trigger $t -Principal $p -Settings $s -Force | Out-Null
  $r = Get-ScheduledTask -TaskName 'ZapretGuiPsTest'
  Write-Output ("PS-REGISTER OK DisallowBattery=" + $r.Settings.DisallowStartIfOnBatteries + " TimeLimit=" + $r.Settings.ExecutionTimeLimit)
  Unregister-ScheduledTask -TaskName 'ZapretGuiPsTest' -Confirm:$false
} catch { Write-Output ("PS-REGISTER FAIL: " + $_.Exception.Message) }`
	out3, err3 := winutil.HiddenCmd("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psScript).CombinedOutput()
	log(f, "Register-ScheduledTask: err=%v out=%q", err3, strings.TrimSpace(string(out3)))
}

// utf16ToUtf8 — schtasks /XML выводит UTF-16LE с BOM.
func utf16ToUtf8(b []byte) []byte {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:i+2]))
	}
	return []byte(string(utf16.Decode(u)))
}

// ---- копии из winutil (приватные там) ----

func currentUserSid() string {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return u.User.Sid.String()
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

func writeUtf16File(path, text string) error {
	u := utf16.Encode([]rune(text))
	buf := make([]byte, 2+2*len(u))
	buf[0], buf[1] = 0xFF, 0xFE
	for i, r := range u {
		binary.LittleEndian.PutUint16(buf[2+2*i:], r)
	}
	return os.WriteFile(path, buf, 0o644)
}

const taskXmlTemplate = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Zapret GUI - autostart (test)</Description>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <IdleSettings>
      <Duration>PT10M</Duration>
      <WaitTimeout>PT1H</WaitTimeout>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
  </Settings>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
    </LogonTrigger>
  </Triggers>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
    </Exec>
  </Actions>
</Task>
`
