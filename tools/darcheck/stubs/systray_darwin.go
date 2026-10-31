//go:build darwin && !cgo

// ЗАГЛУШКА для типовой проверки darwin-сборки на Windows/Linux
// (CGO_ENABLED=0): оригинальный systray_darwin.go — cgo и исключается
// из сборки, а без него пакет не компилируется. Заменяет оригинал через
// go build -overlay (см. tools/darcheck). На реальном Mac НЕ используется.
package systray

import "fmt"

func SetTemplateIcon(templateIconBytes []byte, regularIconBytes []byte) {}
func (item *MenuItem) SetIcon(iconBytes []byte)                         {}
func (item *MenuItem) SetIconFromFilePath(iconFilePath string) error {
	return fmt.Errorf("stub")
}
func (item *MenuItem) SetTemplateIcon(templateIconBytes []byte, regularIconBytes []byte) {}
func SetRemovalAllowed(allowed bool)                                                     {}
func registerSystray()                                                                   {}
func nativeLoop()                                                                        {}
func nativeEnd()                                                                         {}
func nativeStart()                                                                       {}
func quit()                                                                              {}
func setInternalLoop(internal bool)                                                      {}
func SetIcon(iconBytes []byte)                                                           {}
func SetIconFromFilePath(iconFilePath string) error {
	return fmt.Errorf("stub")
}
func SetTitle(title string)                 {}
func SetTooltip(tooltip string)             {}
func addOrUpdateMenuItem(item *MenuItem)    {}
func addSeparator(id uint32, parent uint32) {}
func hideMenuItem(item *MenuItem)           {}
func showMenuItem(item *MenuItem)           {}
func removeMenuItem(item *MenuItem)         {}
func resetMenu()                            {}

func systray_ready()       { systrayReady() }
func systray_on_exit()     { runSystrayExit() }
func systray_left_click()  {}
func systray_right_click() {}
