//go:build windows

package bughub

import "os/exec"

func setCodexProcessGroup(cmd *exec.Cmd) {}

func killCodexProcessGroup(pid int) {}
