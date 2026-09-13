//go:build windows

package main

import "syscall"

// interruptAttr is a no-op on Windows; the end-to-end shutdown test is skipped
// there because os.Interrupt cannot be delivered to a child process.
func interruptAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }
