//go:build !linux && !windows

package main

import "os/exec"

// orphanGuard reports that this platform has no kernel-level guarantee. macOS
// and the BSDs offer no equivalent of Linux's parent-death signal or Windows'
// job objects, so a SIGKILL of glorp there still leaves its subprocesses
// running until they finish on their own.
const orphanGuard = "none"

func guardOrphanedProcess(*exec.Cmd) {}

func adoptOrphanedProcess(*exec.Cmd) {}
