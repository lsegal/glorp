//go:build !linux && !windows

package main

import "os/exec"

// macOS and the BSDs offer no equivalent of Linux's parent-death signal or
// Windows' job objects, so a SIGKILL of glorp there still leaves its
// subprocesses running until they finish on their own. Every exit glorp can
// observe is still cleaned up by the tracker in process.go.
func guardOrphanedProcess(*exec.Cmd) {}

func adoptOrphanedProcess(*exec.Cmd) error { return nil }
