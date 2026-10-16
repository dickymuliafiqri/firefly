package selfupdate

import (
	"log/slog"
)

// Relaunch hands control over to the freshly installed binary.
//
// It is deliberately supervisor-aware. On Unix the new image replaces the
// current one in place with execve, so the process ID and its standard streams
// survive: an operator watching the log sees one continuous process, and a
// service manager that tracks the main PID (systemd, launchd) never observes an
// exit at all — the systemd unit's Restart=always is not even needed.
//
// Windows cannot replace a running image, so there the new binary is started as
// a child and the current process exits. A Windows service is the exception:
// the service control manager is itself the supervisor that would restart the
// binary, so spawning a child as well would leave two instances fighting for
// the listen address. Under a service supervisor Relaunch therefore only
// reports what will happen and returns nil, leaving the actual restart to the
// caller's process exit.
//
// Relaunch must be called only after the server has drained: on Unix it never
// returns on success, and everywhere else the caller is expected to exit as
// soon as it returns nil.
func Relaunch(env Environment, logger *slog.Logger) error {
	if env.Supervisor != SupervisorNone {
		if logger != nil {
			logger.Info("exiting for the supervisor to restart with the new binary",
				"supervisor", string(env.Supervisor), "path", env.Executable)
		}
		return nil
	}
	return relaunchProcess(env, logger)
}
