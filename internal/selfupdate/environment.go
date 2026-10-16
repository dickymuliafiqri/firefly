package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DisableEnvVar forces self-update off when set to a truthy value. It exists for
// images and operators that want a hard guarantee the gateway never rewrites its
// own executable — an immutable base image, for instance — without making the
// feature depend on permissions alone.
const DisableEnvVar = "FIREFLY_DISABLE_SELF_UPDATE"

// Supervisor identifies the init system (if any) that will bring Firefly back
// after it exits to finish an update. It decides the restart strategy: exiting
// cleanly is enough under a supervisor that restarts on exit, while an
// unsupervised process has to relaunch itself.
type Supervisor string

const (
	SupervisorNone    Supervisor = ""
	SupervisorSystemd Supervisor = "systemd"
	SupervisorLaunchd Supervisor = "launchd"
	SupervisorWindows Supervisor = "windows-service"
)

// Environment is what the host allows an in-place update to do. It is computed
// once at startup and reported to the dashboard, so the operator sees why the
// update button is unavailable before clicking it rather than after a partially
// completed download.
type Environment struct {
	// Executable is the resolved path of the running binary. Symlinks are
	// resolved so a Homebrew-style launcher symlink keeps pointing at a path
	// that still exists after the swap.
	Executable string

	// Dir is the directory holding Executable; an update must be able to create
	// files there.
	Dir string

	// Writable reports whether Dir accepted a probe file. A container image or a
	// root-owned /usr/local/bin run by an unprivileged user fails this.
	Writable bool

	// Container reports that the process is running inside a container, where
	// the filesystem is usually ephemeral and the running container is not the
	// one that would restart.
	Container bool

	// Supervisor is who restarts the process after an update, when anyone does.
	Supervisor Supervisor

	// Reason is a human-readable explanation of why self-update is unavailable.
	// Empty means Available() is true.
	Reason string
}

// Available reports whether an in-place update may be attempted.
func (e Environment) Available() bool { return e.Reason == "" }

// RestartBehavior describes, for the confirmation dialog, what will happen to
// the process once the new binary is in place.
func (e Environment) RestartBehavior() string {
	switch e.Supervisor {
	case SupervisorSystemd:
		return "Firefly exits and systemd starts it again with the new binary"
	case SupervisorLaunchd:
		return "Firefly exits and launchd starts it again with the new binary"
	case SupervisorWindows:
		return "Firefly exits and the service manager starts it again with the new binary"
	default:
		return "Firefly relaunches itself with the new binary"
	}
}

// DetectsEnvironment is the injectable form of DetectEnvironment, for tests that
// need to pin the host facts.
type DetectsEnvironment func() Environment

// DetectEnvironment inspects the host once at startup.
//
// The checks are ordered so the most actionable reason wins: a deliberate opt-out
// is reported before a container, which is reported before a permission problem,
// because "you disabled this" and "this container is disposable" are both more
// useful to an operator than "permission denied".
func DetectEnvironment() Environment {
	env := Environment{}

	exe, err := os.Executable()
	if err != nil {
		env.Reason = "the path of the running executable could not be determined"
		return env
	}
	// Resolve symlinks so the swap targets the real file. A failure here is not
	// fatal: the unresolved path is still where the operator expects the binary.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	env.Executable = exe
	env.Dir = filepath.Dir(exe)

	env.Container = runningInContainer()
	env.Supervisor = detectSupervisor()

	switch {
	case isTruthy(os.Getenv(DisableEnvVar)):
		env.Reason = "self-update is disabled by " + DisableEnvVar
	case env.Container:
		env.Reason = "Firefly is running in a container; update the image instead"
	default:
		env.Writable = dirWritable(env.Dir)
		if !env.Writable {
			env.Reason = "the directory holding the executable (" + env.Dir + ") is not writable by this process"
		}
	}

	return env
}

// dirWritable probes the directory by creating and removing a file, which is the
// only reliable check: a read-only mount, a missing write bit, and a sticky
// directory owned by someone else all fail differently but all mean the same
// thing here.
func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".firefly-update-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// runningInContainer looks for the markers every mainstream runtime leaves. The
// cgroup scan catches Go processes started by Kubernetes where neither marker
// file is present.
func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		lower := strings.ToLower(string(data))
		for _, marker := range []string{"docker", "kubepods", "containerd", "lxc", "podman"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return false
}

// detectSupervisor is the portable half of supervisor detection; the
// platform-specific service checks live in supervisor_<goos>.go.
func detectSupervisor() Supervisor {
	if s := detectPlatformSupervisor(); s != SupervisorNone {
		return s
	}
	// systemd sets INVOCATION_ID for every unit it starts, and does not set it
	// for a process the operator ran from a shell. It is more reliable than
	// checking for the systemctl binary or for a cgroup path, both of which vary
	// across distributions and containers.
	if os.Getenv("INVOCATION_ID") != "" {
		return SupervisorSystemd
	}
	// launchd sets XPC_SERVICE_NAME. A plain login shell also sets it to "0", so
	// the literal "0" is excluded.
	if runtime.GOOS == "darwin" {
		if name := os.Getenv("XPC_SERVICE_NAME"); name != "" && name != "0" {
			return SupervisorLaunchd
		}
	}
	return SupervisorNone
}

// isTruthy accepts the values an operator plausibly writes in a compose file or
// unit file. An empty or unrecognized value means "not disabled", so a typo
// never silently turns the feature off.
func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
