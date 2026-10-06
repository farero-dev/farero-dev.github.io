package core

import (
	"os"
	"testing"
	"time"
)

func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid(), time.Now()) {
		t.Fatal("this process should be alive")
	}
	// A process that started after the given time is a different process
	// reusing the PID.
	if processAlive(os.Getpid(), time.Now().Add(-24*time.Hour)) {
		t.Fatal("a process started after the last event must not count")
	}
	if processAlive(99999999, time.Now()) {
		t.Fatal("no such process")
	}
	// launchd is never a session's agent (orphaned hooks report it).
	if processAlive(1, time.Now()) || processAlive(0, time.Now()) {
		t.Fatal("pid 1 and 0 are not agents")
	}
}
