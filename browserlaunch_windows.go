//go:build windows

package main

import (
	"os"
	"path/filepath"
)

// browserBinaryPaths lists the well-known Chrome and Edge install locations on
// Windows, searched after PATH because neither installer puts its executable
// there. Entries whose base directory is unset are returned empty and skipped
// by the caller.
func browserBinaryPaths() []string {
	programFiles := os.Getenv("PROGRAMFILES")
	programFilesX86 := os.Getenv("PROGRAMFILES(X86)")
	localAppData := os.Getenv("LOCALAPPDATA")
	return []string{
		browserInstallPath(programFiles, "Google", "Chrome", "Application", "chrome.exe"),
		browserInstallPath(programFilesX86, "Google", "Chrome", "Application", "chrome.exe"),
		browserInstallPath(localAppData, "Google", "Chrome", "Application", "chrome.exe"),
		browserInstallPath(programFiles, "Chromium", "Application", "chrome.exe"),
		browserInstallPath(localAppData, "Chromium", "Application", "chrome.exe"),
		browserInstallPath(programFiles, "Microsoft", "Edge", "Application", "msedge.exe"),
		browserInstallPath(programFilesX86, "Microsoft", "Edge", "Application", "msedge.exe"),
	}
}

// browserInstallPath joins an install location onto its base directory,
// reporting an empty path when the environment does not define that base.
func browserInstallPath(base string, elements ...string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, elements...)...)
}
