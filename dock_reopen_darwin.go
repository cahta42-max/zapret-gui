//go:build darwin

// dock_reopen_darwin — обработчик клика по иконке Dock. У wails v2 в
// AppDelegate НЕТ applicationShouldHandleReopen:hasVisibleWindows:
// (github.com/wailsapp/wails issue #2437), поэтому при скрытом окне
// (свернули в трей) клик по Dock ничего не показывает. Добавляем метод
// текущему делегату NSApp в рантайме через ObjC runtime (class_addMethod):
// при reopen показываем окно (showMainWindow — колбэк из main).
// Сборка: только darwin, ObjC через cgo.
package main

/*
#cgo CFLAGS: -x objective-c
#import <Cocoa/Cocoa.h>
#import <objc/runtime.h>

extern void zapretDockReopen(void);

// zapretReopenIMP — реализация applicationShouldHandleReopen:hasVisibleWindows:
// Возвращает YES (событие обработано нами, дефолтное поведение не нужно).
static BOOL zapretReopenIMP(id self, SEL _cmd, NSApplication *sender, BOOL hasVisibleWindows) {
	(void)self; (void)_cmd; (void)sender;
	if (!hasVisibleWindows) {
		zapretDockReopen();
	}
	return YES;
}

// zapretInstallReopenHandler — добавляет метод делегату NSApp на главном
// потоке (вызывать можно из любой горутины: уходим в dispatch_async).
static void zapretInstallReopenHandler(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		id delegate = [NSApp delegate];
		if (delegate == nil) return;
		Class cls = object_getClass(delegate);
		if (cls == Nil) return;
		SEL sel = @selector(applicationShouldHandleReopen:hasVisibleWindows:);
		if (class_getInstanceMethod(cls, sel) != NULL) return; // уже есть — не трогаем
		class_addMethod(cls, sel, (IMP)zapretReopenIMP, "B@:@B");
	});
}
*/
import "C"

// installDockReopenHandler вешает reopen-обработчик (после старта NSApp).
func installDockReopenHandler() {
	C.zapretInstallReopenHandler()
}

// zapretDockReopen — показать окно из ObjC (экспорт для cgo).
//
//export zapretDockReopen
func zapretDockReopen() {
	if fn, ok := showMainWindow.Load().(func()); ok && fn != nil {
		fn()
	}
}
