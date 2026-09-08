// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

//go:build linux || darwin

package main

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes the worker the leader of its own process group, so
// the whole tree (dsh and every sandboxed child) can be signalled at once.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the worker's process group.
func signalGroup(pid int, sig syscall.Signal) error {
	return syscall.Kill(-pid, sig)
}
