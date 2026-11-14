package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A glorp.exe that something briefly holds open, as antivirus does right
// after a file is written, is still replaced once it is let go (issue #799).
func TestReplaceExecutableRetriesSharingViolation(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "glorp.exe")
	if err := os.WriteFile(target, []byte("glorp v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "staged")
	if err := os.WriteFile(staged, []byte("glorp v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Go opens files without FILE_SHARE_DELETE, so renaming target fails
	// with a sharing violation while this handle is open.
	held, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+".probe"); err == nil {
		held.Close()
		t.Fatal("rename of an open file succeeded; the test cannot hold it")
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		held.Close()
		close(released)
	}()
	defer func() { <-released }()
	if err := replaceExecutable(target, staged, true); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "glorp v2" {
		t.Fatalf("target = %q, want glorp v2", got)
	}
	if got, _ := os.ReadFile(target + ".bak"); string(got) != "glorp v1" {
		t.Fatalf("backup = %q, want glorp v1", got)
	}
}
