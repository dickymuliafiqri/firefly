package selfupdate

import (
	"path/filepath"
	"testing"
)

// Relaunch's supervisor path must not touch the filesystem at all: the process
// is expected to exit and let the supervisor bring the new binary up.
func TestRelaunchLeavesRestartToTheSupervisor(t *testing.T) {
	for _, sup := range []Supervisor{SupervisorSystemd, SupervisorLaunchd, SupervisorWindows} {
		env := Environment{
			Supervisor: sup,
			Executable: filepath.Join(t.TempDir(), "definitely-missing"),
		}
		if err := Relaunch(env, nil); err != nil {
			t.Fatalf("Relaunch(%q) = %v, want nil", sup, err)
		}
	}
}

// Without a supervisor the new binary is started for real, so a nonexistent
// path must surface as an error instead of silently doing nothing.
func TestRelaunchReportsAFailureToStart(t *testing.T) {
	env := Environment{
		Supervisor: SupervisorNone,
		Executable: filepath.Join(t.TempDir(), "definitely-missing"),
	}
	if err := Relaunch(env, nil); err == nil {
		t.Fatal("Relaunch of a missing binary should have failed")
	}
}
