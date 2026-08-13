package main

import "testing"

func TestParseHostOptions(t *testing.T) {
	opts, err := parseOptions([]string{"--host", "pve-host", "--status"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.host || opts.action != "status" || opts.profile != "pve-host" || opts.ctid != 0 {
		t.Fatalf("unexpected host options: %+v", opts)
	}
}
