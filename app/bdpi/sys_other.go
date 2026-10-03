//go:build !linux && !android

package bdpi

import "syscall"

func sysProcAttr() *syscall.SysProcAttr { return nil }
