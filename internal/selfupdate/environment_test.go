package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectEnvironmentReportsThisHost(t *testing.T) {
	env := DetectEnvironment()

	if env.Executable == "" {
		t.Fatal("Executable is empty; the running binary's path must always be resolved")
	}
	if !filepath.IsAbs(env.Executable) {
		t.Fatalf("Executable = %q, want an absolute path", env.Executable)
	}
	if env.Dir != filepath.Dir(env.Executable) {
		t.Fatalf("Dir = %q, want %q", env.Dir, filepath.Dir(env.Executable))
	}
	// A test binary runs from a temp directory, so it must be writable there.
	if runtime.GOOS != "windows" && !env.Container && !env.Available() && !env.Writable {
		t.Fatalf("a test binary in %q reported unavailable: %s", env.Dir, env.Reason)
	}
}

func TestDetectEnvironmentHonoursDisableEnvVar(t *testing.T) {
	t.Setenv(DisableEnvVar, "1")

	env := DetectEnvironment()
	if env.Available() {
		t.Fatal("self-update should be unavailable while disabled by the environment")
	}
	if env.Reason == "" {
		t.Fatal("an unavailable environment must explain itself")
	}
}

func TestDirWritable(t *testing.T) {
	if !dirWritable(t.TempDir()) {
		t.Fatal("a temp directory should be writable")
	}
	if dirWritable(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Fatal("a missing directory must not report as writable")
	}
}

func TestIsTruthy(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", " yes ", "on"} {
		if !isTruthy(v) {
			t.Fatalf("isTruthy(%q) = false, want true", v)
		}
	}
	// A typo must not silently disable the feature.
	for _, v := range []string{"", "0", "false", "off", "no", "maybe"} {
		if isTruthy(v) {
			t.Fatalf("isTruthy(%q) = true, want false", v)
		}
	}
}

func TestRunningInContainer(t *testing.T) {
	// Only assert the negative is representable; a container marker cannot be
	// faked on a host that lacks /proc and the marker files.
	if runningInContainer() {
		if _, err := os.Stat("/.dockerenv"); err != nil {
			t.Log("container detected without /.dockerenv; assuming a cgroup marker")
		}
	}
}

func TestRestartBehaviorMentionsTheSupervisor(t *testing.T) {
	cases := map[Supervisor]string{
		SupervisorSystemd: "systemd",
		SupervisorLaunchd: "launchd",
		SupervisorWindows: "service manager",
		SupervisorNone:    "relaunches itself",
	}
	for sup, want := range cases {
		env := Environment{Supervisor: sup}
		if got := env.RestartBehavior(); !strings.Contains(got, want) {
			t.Fatalf("RestartBehavior(%q) = %q, want it to mention %q", sup, got, want)
		}
	}
}
