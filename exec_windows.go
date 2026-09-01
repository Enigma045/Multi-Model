//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// setHideWindow configures the process to run silently without opening a console window on Windows
func setHideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
