//go:build !windows

package selfupdate

// detectPlatformSupervisor has nothing platform-specific to add off Windows:
// systemd and launchd are both recognizable from the environment, which
// detectSupervisor already checks. The Windows service control manager is not,
// hence the separate implementation there.
func detectPlatformSupervisor() Supervisor { return SupervisorNone }
