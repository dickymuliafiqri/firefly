//go:build windows

package selfupdate

import "golang.org/x/sys/windows/svc"

// detectPlatformSupervisor reports a Windows service context.
//
// There is no environment variable that reliably distinguishes a service from a
// console process — both are started the same way — so this asks the service
// manager directly. It matters because a service must let the SCM observe its
// exit and restart it, rather than spawning a detached child of its own.
func detectPlatformSupervisor() Supervisor {
	if ok, err := svc.IsWindowsService(); err == nil && ok {
		return SupervisorWindows
	}
	return SupervisorNone
}
