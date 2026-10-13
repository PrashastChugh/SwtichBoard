//go:build !darwin || !cgo

package main

// No native interface on this system: Switchboard runs the server and web dashboard.
const nativeUI = false

func runNativeUI(srv *Server) {}

func showDashboard(url string) { openBrowser(url) }

func sendKeystrokesNative(bundleID, text string) bool { return false }
