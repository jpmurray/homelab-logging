package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalClientPushReplacesFileAtomically(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(source, []byte("managed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("legacy\n"), 0600); err != nil {
		t.Fatal(err)
	}

	client := newLocalClient()
	if err := client.Push(hostTargetID, source, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "managed\n" {
		t.Fatalf("target content = %q", data)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("target mode = %o, want 644", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".homelab-logging-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}
