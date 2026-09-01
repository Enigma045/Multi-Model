//go:build !windows

package main

import "os/exec"

// setHideWindow is a no-op on non-Windows platforms
func setHideWindow(cmd *exec.Cmd) {
	// No-op for Unix/Linux/macOS
}
