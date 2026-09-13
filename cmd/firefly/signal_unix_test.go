//go:build !windows

package main

import "syscall"

// interruptAttr returns SysProcAttr that puts the child in its own process
// group, so a signal we send reaches only the gateway and not the test runner.
func interruptAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
