package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// localClient presents the Proxmox host through the same narrow command and
// file boundary used for containers. The synthetic ID is ignored.
type localClient struct{}

func newLocalClient() *localClient { return &localClient{} }

func (l *localClient) List() ([]Container, error) {
	host, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	return []Container{{ID: hostTargetID, Status: "running", Name: strings.SplitN(host, ".", 2)[0]}}, nil
}

func (l *localClient) Status(int) (string, error) { return "running", nil }

func (l *localClient) Exec(_ int, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing local command")
	}
	cmd := exec.Command(args[0], args[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		details := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
		return stdout.String(), fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, details)
	}
	return stdout.String(), nil
}

func (l *localClient) Pull(_ int, remote, local string) error {
	data, err := os.ReadFile(remote)
	if err != nil {
		return err
	}
	return os.WriteFile(local, data, 0600)
}

func (l *localClient) Push(_ int, local, remote string) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(remote), ".homelab-logging-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0644); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, remote); err != nil {
		return err
	}
	committed = true
	return nil
}
