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
	if len(profiles) != 31 {
		t.Fatalf("got %d profiles, want 31", len(profiles))
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
	config := renderRsyslog(site, siteHash, profile, dockerRuntime{APIVersion: "1.52"}, "patate-du-cluster")
	for _, wanted := range []string{
		"# homelab-logging-profile: docker",
		"# homelab-logging-profile-revision: 2",
		"homelab@32473 cluster=\\\"saint-cluster\\\" location=\\\"home\\\" role=\\\"lxc\\\" node=\\\"patate-du-cluster\\\" job=\\\"docker\\\"",
		"load=\"imdocker\"",
		"ApiVersionStr=\"v1.52\"",
		"PollingInterval=\"5\"",
		`property(name="$.docker_service" position.from="1" position.to="48")`,
		`re_extract($!metadata!Names`,
		`{0,63})\$"`,
		"queue.filename=\"alloy-journal\"",
	} {
		if !strings.Contains(config, wanted) {
			t.Errorf("generated config is missing %q", wanted)
		}
	}
	legacy := profile
	legacy.Docker = Docker{Enabled: true, Mode: "files"}
	first := renderRsyslog(site, siteHash, legacy, dockerRuntime{Sources: []dockerSource{{Service: "z", Path: "/z"}, {Service: "a", Path: "/a"}}}, "node")
	second := renderRsyslog(site, siteHash, legacy, dockerRuntime{Sources: []dockerSource{{Service: "a", Path: "/a"}, {Service: "z", Path: "/z"}}}, "node")
	if first != second {
		t.Error("renderer is not deterministic")
	}
	withoutDockerLogs := renderRsyslog(site, siteHash, legacy, dockerRuntime{}, "node")
	if strings.Contains(withoutDockerLogs, `module(load="imfile"`) || strings.Contains(withoutDockerLogs, `ruleset(name="alloy_docker"`) {
		t.Error("empty Docker discovery generated an imfile pipeline without inputs")
	}
}

func TestDockerAPIModeDiscoversVersionAndInstallsPlugin(t *testing.T) {
	t.Setenv("HLL_TESTING", "1")
	client := newFakeClient()
	client.exists["/var/lib/docker"] = true
	application, out, _ := testApp(t, client, "patate-du-cluster")

	if _, err := application.install(113, "docker"); err != nil {
		t.Fatal(err)
	}
	if !client.packages["rsyslog-docker"] {
		t.Fatal("Docker API mode did not install rsyslog-docker")
	}
	generated := string(client.files[application.site.RemoteConfig])
	for _, wanted := range []string{
		`load="imdocker"`,
		`ApiVersionStr="v1.52"`,
		`PollingInterval="5"`,
		`re_extract($!metadata!Names`,
	} {
		if !strings.Contains(generated, wanted) {
			t.Errorf("generated Docker API configuration is missing %q", wanted)
		}
	}
	if strings.Contains(generated, "/var/lib/docker/containers/") {
		t.Fatal("Docker API mode generated a container-ID file path")
	}
	if !strings.Contains(out.String(), "Installing rsyslog-docker") {
		t.Fatalf("plugin installation was not reported:\n%s", out.String())
	}
	if err := application.status(113, "docker"); err != nil {
		t.Fatalf("healthy Docker API status audit: %v", err)
	}
}

func TestDockerProfileValidation(t *testing.T) {
	profile, err := loadProfile("services/docker.json")
	if err != nil {
		t.Fatal(err)
	}

	invalid := profile
	invalid.Docker.PollingInterval = 0
	if err := invalid.validate(); err == nil {
		t.Fatal("API mode without a polling interval was accepted")
	}

	legacy := profile
	legacy.Docker = Docker{Enabled: true}
	if err := legacy.validate(); err != nil {
		t.Fatalf("legacy files mode should remain valid: %v", err)
	}
	if legacy.Docker.mode() != "files" {
		t.Fatalf("legacy mode resolved to %q, want files", legacy.Docker.mode())
	}
}

func TestVitoProfilePreservesWorkerFilename(t *testing.T) {
	site, siteHash, err := loadSite("config.json")
	if err != nil {
		t.Fatal(err)
	}
	profile, err := loadProfile("services/vito.json")
	if err != nil {
		t.Fatal(err)
	}

	config := renderRsyslog(site, siteHash, profile, dockerRuntime{}, "test-node")
	for _, wanted := range []string{
		`File="/home/vito/vito/storage/logs/laravel.log"`,
		`File="/home/vito/.logs/workers/*.log"`,
		`addMetadata="on"`,
		`Ruleset="alloy_tasks"`,
		`property(name="$.task_id")`,
	} {
		if !strings.Contains(config, wanted) {
			t.Errorf("generated Vito configuration is missing %q", wanted)
		}
	}
}

type fakeClient struct {
	files          map[string][]byte
	exists         map[string]bool
	services       map[string]bool
	packages       map[string]bool
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
		packages:   map[string]bool{},
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
	case "docker":
		if len(args) >= 2 && args[1] == "version" {
			return "1.52\n", nil
		}
		return "", fmt.Errorf("unsupported docker command")
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
		name := args[len(args)-1]
		if f.packages[name] {
			return "installed\n", nil
		}
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
	case "env":
		for _, arg := range args {
			if arg == "rsyslog" || arg == "rsyslog-docker" {
				f.packages[arg] = true
			}
		}
		return "", nil
	case "logger", "apt-get":
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
	base := os.Getenv("HLL_RSYSLOG_TEST_DIR")
	work, err := os.MkdirTemp(base, "homelab-logging-rsyslog-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(work) })
	// Ubuntu's rsyslogd may validate as its unprivileged service user, so make
	// this disposable directory traversable.
	if err := os.Chmod(work, 0755); err != nil {
		t.Fatal(err)
	}
	workDirectory := work
	if base != "" {
		workDirectory = "/var/spool/rsyslog"
	}
	imdockerAvailable := false
	for _, pattern := range []string{
		"/usr/lib/rsyslog/imdocker.so",
		"/usr/lib/*/rsyslog/imdocker.so",
		"/usr/local/lib/rsyslog/imdocker.so",
		"/usr/local/lib/*/rsyslog/imdocker.so",
	} {
		matches, globErr := filepath.Glob(pattern)
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) > 0 {
			imdockerAvailable = true
			break
		}
	}
	for _, profile := range profiles {
		if profile.Docker.Enabled && profile.Docker.mode() == "api" && !imdockerAvailable {
			t.Logf("%s: skipping rsyslogd integration validation because imdocker is unavailable", profile.Name)
			continue
		}
		runtime := dockerRuntime{}
		if profile.Docker.Enabled && profile.Docker.mode() == "api" {
			runtime.APIVersion = "1.52"
		}
		config := fmt.Sprintf("global(workDirectory=\"%s\")\n%s", workDirectory, renderRsyslog(site, siteHash, profile, runtime, "test-node"))
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
