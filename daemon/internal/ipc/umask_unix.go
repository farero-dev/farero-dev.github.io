//go:build unix

package ipc

import "syscall"

func umask(m int) int { return syscall.Umask(m) }
