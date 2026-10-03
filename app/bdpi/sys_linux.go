//go:build linux || android

package bdpi

import "syscall"

// ByeDPI умирает вместе с ядром приложения
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL} }
