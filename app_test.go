package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryConfiguration(t *testing.T) {
	site, _, err := loadSite("config.json")
	if err != nil {
		t.Fatal(err)
	}
	if site.Cluster != "saint-cluster" {
		t.Fatalf("unexpected cluster %q", site.Cluster)
	}
	profiles, err := loadProfiles("services")
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 22 {
		t.Fatalf("got %d profiles, want 22", len(profiles))
	}
}

func TestRenderRsyslog(t *testing.T) {
	site, siteHash, err := loadSite("config.json")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := loadProfile("services/docker.json")
	if err != nil {
		t.Fatal(err)
	}
	config := renderRsyslog(site, siteHash, profile, []dockerSource{{Service: "mealie", Path: "/var/lib/docker/containers/abc/abc-json.log"}}, "patate-du-cluster")
	for _, wanted := range []string{
		"# homelab-logging-profile: docker",
		"# homelab-logging-profile-revision: 1",
		"homelab@32473 cluster=\\\"saint-cluster\\\" location=\\\"home\\\" role=\\\"lxc\\\" node=\\\"patate-du-cluster\\\" job=\\\"docker\\\"",
		"File=\"/var/lib/docker/containers/abc/abc-json.log\"",
		"Tag=\"mealie:\"",
		"queue.filename=\"alloy-journal\"",
	} {
		if !strings.Contains(config, wanted) {
			t.Errorf("generated config is missing %q", wanted)
		}
	}
	first := renderRsyslog(site, siteHash, profile, []dockerSource{{Service: "z", Path: "/z"}, {Service: "a", Path: "/a"}}, "node")
	second := renderRsyslog(site, siteHash, profile, []dockerSource{{Service: "a", Path: "/a"}, {Service: "z", Path: "/z"}}, "node")
	if first != second {
		t.Error("renderer is not deterministic")
	}
}

type fakeClient struct {
	files          map[string][]byte
	exists         map[string]bool
	services       map[string]bool
	dockerOutput   string
	containers     []Container
	failValidation bool
	restarts       int
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		files:      map[string][]byte{},
		exists:     map[string]bool{},
		services:   map[string]bool{},
		containers: []Container{{ID: 105, Status: "running", Name: "postgres"}},
	}
}

func (f *fakeClient) List() ([]Container, error) { return f.containers, nil }
func (f *fakeClient) Status(int) (string, error) { return "running", nil }

func (f *fakeClient) Exec(_ int, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing command")
	}
	switch args[0] {
	case "hostname":
		return "postgres\n", nil
	case "test":
		path := args[2]
		if _, ok := f.files[path]; ok || f.exists[path] {
			return "", nil
		}
		return "", fmt.Errorf("missing %s", path)
	case "systemctl":
		switch args[1] {
		case "cat":
			if f.services[args[2]] {
				return "", nil
			}
			return "", fmt.Errorf("service missing")
		case "restart":
			f.restarts++
			return "", nil
		default:
			return "", nil
		}
	case "sh":
		if strings.Contains(args[2], "docker inspect") {
			return f.dockerOutput, nil
		}
		return "", nil
	case "bash":
		script := args[2]
		if strings.Contains(script, "compgen") {
			pattern := args[len(args)-1]
			for path := range f.files {
				matched, _ := filepath.Match(pattern, path)
				if matched {
					return "", nil
				}
			}
			for path := range f.exists {
				matched, _ := filepath.Match(pattern, path)
				if matched {
					return "", nil
				}
			}
			return "", fmt.Errorf("no match")
		}
		return "", nil
	case "dpkg-query":
		return "", fmt.Errorf("package missing")
	case "cp":
		data, ok := f.files[args[1]]
		if !ok {
			return "", fmt.Errorf("source missing")
		}
		f.files[args[2]] = append([]byte(nil), data...)
		return "", nil
	case "mv":
		data, ok := f.files[args[1]]
		if !ok {
			return "", fmt.Errorf("source missing")
		}
		f.files[args[2]] = data
		delete(f.files, args[1])
		return "", nil
	case "rm":
		delete(f.files, args[len(args)-1])
		return "", nil
	case "rsyslogd":
		if f.failValidation {
			return "", fmt.Errorf("invalid")
		}
		return "", nil
	case "logger", "apt-get", "env":
		return "", nil
	default:
		return "", fmt.Errorf("unsupported command %q", args[0])
	}
}

func (f *fakeClient) Pull(_ int, remote, local string) error {
	data, ok := f.files[remote]
	if !ok {
		return os.ErrNotExist
	}
	return os.WriteFile(local, data, 0644)
}

func (f *fakeClient) Push(_ int, local, remote string) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	f.files[remote] = append([]byte(nil), data...)
	return nil
}

func testApp(t *testing.T, client *fakeClient, node string) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	site, siteHash, err := loadSite("config.json")
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := loadProfiles("services")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	return newApp(site, siteHash, profiles, client, node, &out, &errOut), &out, &errOut
}

func TestGeneratedConfigurationsAreAcceptedByRsyslog(t *testing.T) {
	binary, err := exec.LookPath("rsyslogd")
	if err != nil {
		t.Skip("rsyslogd is not installed")
	}
	site, siteHash, err := loadSite("config.json")
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := loadProfiles("services")
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp("", "homelab-logging-rsyslog-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	// Ubuntu's rsyslogd may validate as its unprivileged service user, so make
	// this disposable directory traversable.
	if err := os.Chmod(work, 0755); err != nil {
		t.Fatal(err)
	}
	for _, profile := range profiles {
		config := fmt.Sprintf("global(workDirectory=\"%s\")\n%s", work, renderRsyslog(site, siteHash, profile, nil, "test-node"))
		path := filepath.Join(work, profile.Name+".conf")
		if err := os.WriteFile(path, []byte(config), 0644); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(binary, "-N1", "-f", path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("%s: %v\n%s", profile.Name, err, output)
		}
	}
}

func TestDetectionDeploymentIdempotencyAndRollback(t *testing.T) {
	t.Setenv("HLL_TESTING", "1")
	client := newFakeClient()
	client.services["postgresql.service"] = true
	client.exists["/var/log/postgresql"] = true
	client.exists["/var/log/postgresql/postgresql-17-main.log"] = true
	client.files["/etc/rsyslog.d/90-alloy.conf"] = []byte("legacy forwarding\n")
	application, out, _ := testApp(t, client, "patate-du-cluster")

	profile, err := application.detectProfile(105)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "postgres" {
		t.Fatalf("detected %q, want postgres", profile.Name)
	}
	if _, err := application.install(105, ""); err != nil {
		t.Fatal(err)
	}
	managed := application.site.RemoteConfig
	first := append([]byte(nil), client.files[managed]...)
	if !strings.Contains(string(first), "# homelab-logging-node: patate-du-cluster") {
		t.Fatal("managed configuration lacks node marker")
	}
	if _, active := client.files["/etc/rsyslog.d/90-alloy.conf"]; active {
		t.Fatal("legacy configuration remains active")
	}
	if !strings.Contains(out.String(), "Detected profile: postgres") {
		t.Fatal("auto-detection was not reported")
	}

	restarts := client.restarts
	if result, err := application.install(105, "postgres"); err != nil || !result.Current {
		t.Fatalf("idempotent install: current=%v err=%v", result.Current, err)
	}
	if client.restarts != restarts {
		t.Fatal("idempotent install restarted rsyslog")
	}
	if err := application.status(105, "postgres"); err != nil {
		t.Fatalf("healthy status audit: %v", err)
	}

	client.files["/etc/rsyslog.d/90-alloy.conf"] = []byte("legacy replacement\n")
	client.failValidation = true
	migrated, _, _ := testApp(t, client, "new-node")
	if _, err := migrated.install(105, "postgres"); err == nil {
		t.Fatal("invalid candidate unexpectedly succeeded")
	}
	if !bytes.Equal(client.files[managed], first) {
		t.Fatal("rollback did not restore managed configuration")
	}
	if string(client.files["/etc/rsyslog.d/90-alloy.conf"]) != "legacy replacement\n" {
		t.Fatal("rollback did not reactivate legacy configuration")
	}
}

func TestInventoryRecognizesRevisionDrift(t *testing.T) {
	client := newFakeClient()
	application, _, _ := testApp(t, client, "node")
	client.files[application.site.RemoteConfig] = []byte("# Managed\n# homelab-logging-profile: postgres\n# homelab-logging-profile-revision: 1\n# homelab-logging-node: node\n# homelab-logging-profile-sha256: different\n")
	var out bytes.Buffer
	application.out = &out
	if err := application.reconcile("inventory", "postgres"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "hash drift at same revision") {
		t.Fatalf("inventory output did not report drift:\n%s", out.String())
	}
}
