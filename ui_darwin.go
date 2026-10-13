//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework WebKit -framework ApplicationServices
#include <stdlib.h>
#include "ui_darwin_impl.h"
*/
import "C"

import (
	"encoding/json"
	"runtime"
	"unsafe"
)

// macOS requires the interface to run on the main thread.
func init() { runtime.LockOSThread() }

const nativeUI = true

var activeServer *Server

type macUI struct{}

func (macUI) Update(state []byte) {
	cs := C.CString(string(state))
	defer C.free(unsafe.Pointer(cs))
	C.SBUpdate(cs) // copies the string, then draws on the main thread
}

func (macUI) Toast(title, body, sessionID string) {
	ct, cb, cs := C.CString(title), C.CString(body), C.CString(sessionID)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cb))
	defer C.free(unsafe.Pointer(cs))
	C.SBToast(ct, cb, cs)
}

func cbool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// showDashboard opens the dashboard in an in-app window instead of the
// system browser, so Switchboard has no dependency on one being installed.
func showDashboard(url string) {
	cs := C.CString(url)
	defer C.free(unsafe.Pointer(cs))
	C.SBShowDashboard(cs)
}

// sendKeystrokesNative types text into the given app via Core Graphics
// events from inside this process - so the Accessibility permission this
// needs is scoped to switchboard itself, not a shared system interpreter.
func sendKeystrokesNative(bundleID, text string) bool {
	cb, ct := C.CString(bundleID), C.CString(text)
	defer C.free(unsafe.Pointer(cb))
	defer C.free(unsafe.Pointer(ct))
	return C.SBSendKeystrokes(cb, ct) != 0
}

// runNativeUI shows the widget, cards and menu bar icon. Blocks until you quit.
func runNativeUI(srv *Server) {
	activeServer = srv
	srv.store.SetUI(macUI{})
	initial, _ := json.Marshal(srv.store.UIState())
	ci := C.CString(string(initial))
	defer C.free(unsafe.Pointer(ci))
	wx, wy := -1.0, -1.0
	if len(srv.cfg.WidgetPos) == 2 {
		wx, wy = srv.cfg.WidgetPos[0], srv.cfg.WidgetPos[1]
	}
	C.SBRun(ci, C.double(wx), C.double(wy), cbool(srv.cfg.Widget), cbool(srv.cfg.Popups))
}
