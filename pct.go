package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Container struct {
	ID     int
	Status string
	Name   string
}

type containerClient interface {
	List() ([]Container, error)
	Status(int) (string, error)
	Exec(int, ...string) (string, error)
	Pull(int, string, string) error
	Push(int, string, string) error
}

type pctClient struct {
	binary string
}

func newPCTClient(binary string) *pctClient {
	if binary == "" {
		binary = "pct"
	}
	return &pctClient{binary: binary}
}

func (p *pctClient) command(args ...string) (string, error) {
	cmd := exec.Command(p.binary, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err != nil {
		return output.String(), fmt.Errorf("%s %s: %w: %s", p.binary, strings.Join(args, " "), err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

func (p *pctClient) List() ([]Container, error) {
	output, err := p.command("list")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	containers := make([]Container, 0, len(lines))
	for i, line := range lines {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		id, err := strconv.Atoi(fields[0])
		if err != nil || id < 1 {
			continue
		}
		name := ""
		if len(fields) >= 4 {
			name = fields[3]
		}
		containers = append(containers, Container{ID: id, Status: fields[1], Name: name})
	}
	return containers, nil
}

func (p *pctClient) Status(ctid int) (string, error) {
	output, err := p.command("status", strconv.Itoa(ctid))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) < 2 {
		return "", fmt.Errorf("unexpected pct status output: %q", strings.TrimSpace(output))
	}
	return fields[len(fields)-1], nil
}

func (p *pctClient) Exec(ctid int, args ...string) (string, error) {
	pctArgs := []string{"exec", strconv.Itoa(ctid), "--"}
	pctArgs = append(pctArgs, args...)
	return p.command(pctArgs...)
}

func (p *pctClient) Pull(ctid int, remote, local string) error {
	_, err := p.command("pull", strconv.Itoa(ctid), remote, local)
	return err
}

func (p *pctClient) Push(ctid int, local, remote string) error {
	_, err := p.command("push", strconv.Itoa(ctid), local, remote, "--perms", "0644")
	return err
}

func requirePCT(binary string) error {
	if strings.ContainsRune(binary, os.PathSeparator) {
		info, err := os.Stat(binary)
		if err != nil || info.Mode()&0111 == 0 {
			return fmt.Errorf("pct executable not found: %s", binary)
		}
		return nil
	}
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("pct was not found; run this on a Proxmox VE host")
	}
	return nil
}
