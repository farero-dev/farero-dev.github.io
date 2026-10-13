package core

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// processAlive reports whether pid is a running process that started no
// later than before. A process started afterwards only reuses the PID of
// the one we knew.
func processAlive(pid int, before time.Time) bool {
	if !processExists(pid) {
		return false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return true // exists; start time unknown
	}
	return !timevalTime(kp.Proc.P_starttime).After(before)
}

// timevalTime converts the kernel's start time (seconds and microseconds).
func timevalTime(tv unix.Timeval) time.Time {
	return time.Unix(tv.Sec, int64(tv.Usec)*1000)
}

// processExists reports whether pid is a running process other than
// launchd (orphaned hooks report PID 1).
func processExists(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
