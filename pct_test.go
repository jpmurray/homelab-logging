package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPCTCommandKeepsSuccessfulStderrOutOfMachineOutput(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "pct")
	script := `#!/bin/sh
printf '%s\n' 'perl: warning: Setting locale failed.' >&2
printf '%s\n' '1.55'
`
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	output, err := newPCTClient(binary).command("exec", "126", "--", "docker", "version")
	if err != nil {
		t.Fatal(err)
	}
	if output != "1.55\n" {
		t.Fatalf("successful command output = %q, want only stdout", output)
	}
}

func TestPCTCommandIncludesStderrWhenCommandFails(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "pct")
	script := `#!/bin/sh
printf '%s\n' 'partial output'
printf '%s\n' 'failure detail' >&2
exit 7
`
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	output, err := newPCTClient(binary).command("status", "126")
	if err == nil {
		t.Fatal("failed command returned no error")
	}
	if output != "partial output\n" {
		t.Fatalf("failed command stdout = %q", output)
	}
	if !strings.Contains(err.Error(), "partial output") || !strings.Contains(err.Error(), "failure detail") {
		t.Fatalf("error lacks command diagnostics: %v", err)
	}
}
