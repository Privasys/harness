// Copyright (c) Privasys. All rights reserved.
// Licensed under the GNU Affero General Public License v3.0.

//go:build !linux && !darwin

package main

import (
	"os/exec"
	"syscall"
)

// Workers run only inside the Linux enclave image; these stubs keep the
// package building on a developer's machine.
func setProcessGroup(*exec.Cmd) {}

func signalGroup(int, syscall.Signal) error { return nil }
