package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var dockerAPIVersionPattern = regexp.MustCompile(`^1[.][0-9]+$`)

type app struct {
	site          SiteConfig
	siteHash      string
	profiles      []Profile
	profileByName map[string]Profile
	pct           containerClient
	node          string
	out           io.Writer
	errOut        io.Writer
	dryRun        bool
}

func newApp(site SiteConfig, siteHash string, profiles []Profile, pct containerClient, node string, out, errOut io.Writer) *app {
	byName := make(map[string]Profile, len(profiles))
	for _, profile := range profiles {
		byName[profile.Name] = profile
	}
	return &app{site: site, siteHash: siteHash, profiles: profiles, profileByName: byName, pct: pct, node: node, out: out, errOut: errOut}
}

func (a *app) info(format string, args ...any) {
	fmt.Fprintf(a.out, "==> "+format+"\n", args...)
}

func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.errOut, "WARNING: "+format+"\n", args...)
}

func (a *app) profile(name string) (Profile, error) {
	profile, ok := a.profileByName[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown profile %q; run --list to see available profiles", name)
	}
	return profile, nil
}

func (a *app) verifyContainer(ctid int) error {
	if ctid < 1 {
		return fmt.Errorf("CTID must be a positive integer")
	}
	status, err := a.pct.Status(ctid)
	if err != nil {
		return fmt.Errorf("container %d does not exist: %w", ctid, err)
	}
	if status != "running" {
		return fmt.Errorf("container %d is not running (status: %s)", ctid, status)
	}
	return nil
}

func (a *app) remoteFileExists(ctid int, path string) bool {
	_, err := a.pct.Exec(ctid, "test", "-f", path)
	return err == nil
}

func (a *app) patternExists(ctid int, pattern string) bool {
	_, err := a.pct.Exec(ctid, "bash", "-c", `compgen -G "$1" | grep -q .`, "_", pattern)
	return err == nil
}

func (a *app) pullTemp(ctid int, remote string) (string, error) {
	file, err := os.CreateTemp("", "homelab-logging-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := a.pct.Pull(ctid, remote, path); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func marker(data []byte, name string) string {
	prefix := "# " + name + ": "
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func (a *app) installedMetadata(ctid int) (map[string]string, error) {
	if !a.remoteFileExists(ctid, a.site.RemoteConfig) {
		return nil, os.ErrNotExist
	}
	path, err := a.pullTemp(ctid, a.site.RemoteConfig)
	if err != nil {
		return nil, err
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"profile":      marker(data, "homelab-logging-profile"),
		"revision":     marker(data, "homelab-logging-profile-revision"),
		"node":         marker(data, "homelab-logging-node"),
		"profile_hash": marker(data, "homelab-logging-profile-sha256"),
	}, nil
}

func (a *app) probeMatches(ctid int, probe Probe) bool {
	var err error
	switch probe.Type {
	case "path":
		_, err = a.pct.Exec(ctid, "test", "-e", probe.Value)
	case "service":
		_, err = a.pct.Exec(ctid, "systemctl", "cat", probe.Value)
	case "command":
		_, err = a.pct.Exec(ctid, "sh", "-c", `command -v "$1" >/dev/null 2>&1`, "_", probe.Value)
	case "package":
		_, err = a.pct.Exec(ctid, "dpkg-query", "-W", probe.Value)
	default:
		return false
	}
	return err == nil
}

func (a *app) detectProfile(ctid int) (Profile, error) {
	bestScore := -1
	var best []Profile
	for _, profile := range a.profiles {
		total := len(profile.Detect.Probes)
		if total == 0 {
			continue
		}
		matched := 0
		for _, probe := range profile.Detect.Probes {
			if a.probeMatches(ctid, probe) {
				matched++
			}
		}
		ok := profile.Detect.Mode == "all" && matched == total || profile.Detect.Mode == "any" && matched > 0
		if !ok {
			continue
		}
		score := profile.Detect.Priority*100 + matched
		if score > bestScore {
			bestScore = score
			best = []Profile{profile}
		} else if score == bestScore {
			best = append(best, profile)
		}
	}
	if len(best) == 0 {
		return Profile{}, fmt.Errorf("no profile matched CT %d; specify one explicitly", ctid)
	}
	if len(best) > 1 {
		names := make([]string, len(best))
		for i := range best {
			names[i] = best[i].Name
		}
		return Profile{}, fmt.Errorf("profile detection is ambiguous: %s", strings.Join(names, " "))
	}
	return best[0], nil
}

func (a *app) dockerRuntime(ctid int, profile Profile) (dockerRuntime, error) {
	if !profile.Docker.Enabled {
		return dockerRuntime{}, nil
	}
	if profile.Docker.mode() == "api" {
		output, err := a.pct.Exec(ctid, "docker", "version", "--format", "{{.Server.APIVersion}}")
		if err != nil {
			return dockerRuntime{}, fmt.Errorf("discover Docker API version in CT %d: %w", ctid, err)
		}
		apiVersion := strings.TrimSpace(output)
		if !dockerAPIVersionPattern.MatchString(apiVersion) {
			return dockerRuntime{}, fmt.Errorf("Docker returned invalid API version %q in CT %d", apiVersion, ctid)
		}
		return dockerRuntime{APIVersion: apiVersion}, nil
	}
	output, err := a.pct.Exec(ctid, "sh", "-c", `
for id in $(docker ps -aq 2>/dev/null); do
    docker inspect --format "{{.Name}}|{{.LogPath}}" "$id"
done`)
	if err != nil {
		return dockerRuntime{}, nil
	}
	seen := map[string]bool{}
	var sources []dockerSource
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(parts) != 2 {
			continue
		}
		service := strings.TrimPrefix(parts[0], "/")
		path := parts[1]
		key := service + "|" + path
		if !safeName.MatchString(service) || !safeAbsolutePath(path) || strings.Contains(path, `\`) || seen[key] {
			continue
		}
		seen[key] = true
		sources = append(sources, dockerSource{Service: service, Path: path})
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Service == sources[j].Service {
			return sources[i].Path < sources[j].Path
		}
		return sources[i].Service < sources[j].Service
	})
	return dockerRuntime{Sources: sources}, nil
}

func (a *app) checkRequired(ctid int, profile Profile) error {
	var failures []string
	for _, path := range profile.RequiredPaths {
		if _, err := a.pct.Exec(ctid, "test", "-e", path); err != nil {
			failures = append(failures, fmt.Sprintf("missing required path in CT %d: %s", ctid, path))
		}
	}
	for _, source := range append(append([]Source{}, profile.Files...), profile.Tasks...) {
		if source.Required && !a.patternExists(ctid, source.Path) {
			failures = append(failures, fmt.Sprintf("required log source has no matches in CT %d: %s", ctid, source.Path))
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "\n"))
	}
	return nil
}

func (a *app) activeLegacy(ctid int) []string {
	var active []string
	for _, path := range a.site.LegacyConfigs {
		if path != a.site.RemoteConfig && a.remoteFileExists(ctid, path) {
			active = append(active, path)
		}
	}
	return active
}

func (a *app) dockerPluginInstalled(ctid int) bool {
	output, err := a.pct.Exec(ctid, "dpkg-query", "-W", "-f=${db:Status-Status}", "rsyslog-docker")
	return err == nil && strings.TrimSpace(output) == "installed"
}

func (a *app) ensureRsyslog(ctid int, profile Profile) error {
	var missing []string
	if _, err := a.pct.Exec(ctid, "sh", "-c", "command -v rsyslogd >/dev/null 2>&1"); err != nil {
		missing = append(missing, "rsyslog")
	}
	if profile.Docker.Enabled && profile.Docker.mode() == "api" {
		if !a.dockerPluginInstalled(ctid) {
			missing = append(missing, "rsyslog-docker")
		}
	}
	if len(missing) == 0 {
		return nil
	}
	a.info("Installing %s in CT %d", strings.Join(missing, " and "), ctid)
	if _, err := a.pct.Exec(ctid, "apt-get", "update"); err != nil {
		return err
	}
	args := []string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y"}
	args = append(args, missing...)
	execArgs := append([]string{"env"}, args...)
	_, err := a.pct.Exec(ctid, execArgs...)
	return err
}

type deploymentResult struct {
	Current bool
}

func (a *app) deploy(ctid int, profile Profile, generated []byte) (result deploymentResult, err error) {
	lockPath, err := acquireLock(ctid)
	if err != nil {
		return result, err
	}
	defer os.Remove(lockPath)

	if err = a.ensureRsyslog(ctid, profile); err != nil {
		return result, err
	}
	if err = a.checkRequired(ctid, profile); err != nil {
		return result, err
	}

	currentPath := ""
	currentExists := a.remoteFileExists(ctid, a.site.RemoteConfig)
	if currentExists {
		currentPath, err = a.pullTemp(ctid, a.site.RemoteConfig)
		if err != nil {
			return result, err
		}
		defer os.Remove(currentPath)
		current, readErr := os.ReadFile(currentPath)
		if readErr != nil {
			return result, readErr
		}
		if bytes.Equal(current, generated) && len(a.activeLegacy(ctid)) == 0 {
			a.info("Configuration is already current for profile %s", profile.Name)
			_, err = a.pct.Exec(ctid, "systemctl", "enable", "--now", "rsyslog")
			result.Current = err == nil
			return result, err
		}
	}

	stamp := time.Now().Format("20060102-150405") + "-" + strconv.Itoa(os.Getpid())
	backup := ""
	if currentExists {
		backup = a.site.RemoteConfig + ".bak." + stamp
		if _, err = a.pct.Exec(ctid, "cp", a.site.RemoteConfig, backup); err != nil {
			return result, err
		}
		a.info("Backup created: %s", backup)
	}
	type movedFile struct{ original, disabled string }
	var moved []movedFile
	rollback := func() {
		a.warn("Rolling back rsyslog configuration in CT %d", ctid)
		if currentExists {
			_, _ = a.pct.Exec(ctid, "cp", backup, a.site.RemoteConfig)
		} else {
			_, _ = a.pct.Exec(ctid, "rm", "-f", a.site.RemoteConfig)
		}
		for _, file := range moved {
			if a.remoteFileExists(ctid, file.disabled) {
				_, _ = a.pct.Exec(ctid, "mv", file.disabled, file.original)
			}
		}
		_, _ = a.pct.Exec(ctid, "systemctl", "restart", "rsyslog")
	}
	committed := false
	defer func() {
		if err != nil && !committed {
			rollback()
		}
	}()

	for _, legacy := range a.activeLegacy(ctid) {
		disabled := legacy + ".disabled." + stamp
		if _, err = a.pct.Exec(ctid, "mv", legacy, disabled); err != nil {
			return result, err
		}
		moved = append(moved, movedFile{legacy, disabled})
		a.info("Legacy configuration disabled and preserved: %s", disabled)
	}

	candidate, err := os.CreateTemp("", "homelab-logging-generated-*")
	if err != nil {
		return result, err
	}
	candidatePath := candidate.Name()
	defer os.Remove(candidatePath)
	if _, err = candidate.Write(generated); err != nil {
		candidate.Close()
		return result, err
	}
	if err = candidate.Close(); err != nil {
		return result, err
	}
	if err = a.pct.Push(ctid, candidatePath, a.site.RemoteConfig); err != nil {
		return result, err
	}
	if _, err = a.pct.Exec(ctid, "rsyslogd", "-N1"); err != nil {
		return result, fmt.Errorf("rsyslog validation failed; the previous configuration will be restored: %w", err)
	}
	if _, err = a.pct.Exec(ctid, "systemctl", "enable", "rsyslog"); err != nil {
		return result, fmt.Errorf("could not enable rsyslog: %w", err)
	}
	if _, err = a.pct.Exec(ctid, "systemctl", "restart", "rsyslog"); err != nil {
		return result, fmt.Errorf("rsyslog restart failed: %w", err)
	}
	if _, err = a.pct.Exec(ctid, "systemctl", "is-active", "--quiet", "rsyslog"); err != nil {
		return result, fmt.Errorf("rsyslog is not active after restart: %w", err)
	}
	committed = true
	_, _ = a.pct.Exec(ctid, "logger", "-t", profile.TestService, fmt.Sprintf("homelab-logging v%s forwarding test for profile %s", version, profile.Name))
	a.info("Installed profile %s in CT %d", profile.Name, ctid)
	host, _ := a.pct.Exec(ctid, "hostname")
	fmt.Fprintf(a.out, "Suggested LogQL: {cluster=\"%s\", host=\"%s\"}\n", a.site.Cluster, strings.TrimSpace(host))
	return result, nil
}

func acquireLock(ctid int) (string, error) {
	root := "/run/lock"
	if os.Getenv("HLL_TESTING") == "1" {
		root = os.TempDir()
	}
	path := filepath.Join(root, fmt.Sprintf("homelab-logging-%d.lock", ctid))
	if err := os.Mkdir(path, 0755); err != nil {
		return "", fmt.Errorf("another homelab-logging operation is active for CT %d", ctid)
	}
	return path, nil
}

func (a *app) resolveProfile(ctid int, name string) (Profile, error) {
	if name != "" {
		return a.profile(name)
	}
	a.info("Detecting profile for CT %d", ctid)
	profile, err := a.detectProfile(ctid)
	if err != nil {
		return Profile{}, err
	}
	a.info("Detected profile: %s", profile.Name)
	return profile, nil
}

func (a *app) install(ctid int, profileName string) (deploymentResult, error) {
	if err := a.verifyContainer(ctid); err != nil {
		return deploymentResult{}, err
	}
	profile, err := a.resolveProfile(ctid, profileName)
	if err != nil {
		return deploymentResult{}, err
	}
	docker, err := a.dockerRuntime(ctid, profile)
	if err != nil {
		return deploymentResult{}, err
	}
	generated := []byte(renderRsyslog(a.site, a.siteHash, profile, docker, a.node))
	if err := a.checkRequired(ctid, profile); err != nil {
		return deploymentResult{}, err
	}
	if a.dryRun {
		fmt.Fprintf(a.out, "Dry run for CT %d using profile %s\n", ctid, profile.Name)
		fmt.Fprintf(a.out, "Would install rsyslog if absent, back up %s, validate, restart, and send a test message.\n", a.site.RemoteConfig)
		for _, legacy := range a.activeLegacy(ctid) {
			fmt.Fprintf(a.out, "Would back up and deactivate legacy configuration: %s\n", legacy)
		}
		fmt.Fprintln(a.out)
		fmt.Fprintln(a.out, strings.Repeat("-", 64))
		fmt.Fprint(a.out, string(generated))
		fmt.Fprintln(a.out, strings.Repeat("-", 64))
		a.printNotes(profile)
		return deploymentResult{}, nil
	}
	result, err := a.deploy(ctid, profile, generated)
	if err == nil {
		a.printNotes(profile)
	}
	return result, err
}

func (a *app) migrate(ctid int) error {
	if err := a.verifyContainer(ctid); err != nil {
		return err
	}
	metadata, err := a.installedMetadata(ctid)
	if err != nil || metadata["profile"] == "" {
		return fmt.Errorf("CT %d has no managed profile to migrate; run a normal installation first", ctid)
	}
	a.info("Refreshing CT %d after migration: node %s -> %s (profile: %s)", ctid, defaultString(metadata["node"], "unknown"), a.node, metadata["profile"])
	if _, err := a.install(ctid, metadata["profile"]); err != nil {
		return err
	}
	if !a.dryRun {
		a.info("Migration refresh complete: CT %d is labeled with node=%s", ctid, a.node)
	}
	return nil
}

func (a *app) status(ctid int, profileName string) error {
	if err := a.verifyContainer(ctid); err != nil {
		return err
	}
	if profileName == "" {
		if metadata, err := a.installedMetadata(ctid); err == nil {
			profileName = metadata["profile"]
			if _, known := a.profileByName[profileName]; profileName != "" && !known {
				a.warn("Installed configuration names unknown profile %q; using auto-detection for comparison", profileName)
				profileName = ""
			}
		}
	}
	profile, err := a.resolveProfile(ctid, profileName)
	if err != nil {
		return err
	}
	docker, err := a.dockerRuntime(ctid, profile)
	if err != nil {
		return err
	}
	generated := []byte(renderRsyslog(a.site, a.siteHash, profile, docker, a.node))
	host, _ := a.pct.Exec(ctid, "hostname")
	fmt.Fprintf(a.out, "Audit for CT %d (%s)\n%s\n", ctid, strings.TrimSpace(host), strings.Repeat("-", 64))
	problems := 0
	check := func(ok bool, good, bad string) {
		if ok {
			fmt.Fprintf(a.out, "[ok]   %s\n", good)
		} else {
			fmt.Fprintf(a.out, "[fail] %s\n", bad)
			problems++
		}
	}
	_, err = a.pct.Exec(ctid, "sh", "-c", "command -v rsyslogd >/dev/null 2>&1")
	check(err == nil, "rsyslog is installed", "rsyslog is not installed")
	if profile.Docker.Enabled && profile.Docker.mode() == "api" {
		check(a.dockerPluginInstalled(ctid), "rsyslog Docker API input is installed", "rsyslog Docker API input is not installed")
	}
	_, err = a.pct.Exec(ctid, "systemctl", "is-active", "--quiet", "rsyslog")
	check(err == nil, "rsyslog is running", "rsyslog is not running")

	if a.remoteFileExists(ctid, a.site.RemoteConfig) {
		path, pullErr := a.pullTemp(ctid, a.site.RemoteConfig)
		if pullErr != nil {
			return pullErr
		}
		data, readErr := os.ReadFile(path)
		os.Remove(path)
		if readErr != nil {
			return readErr
		}
		actualProfile := marker(data, "homelab-logging-profile")
		actualRevision := marker(data, "homelab-logging-profile-revision")
		actualNode := marker(data, "homelab-logging-node")
		check(actualProfile == profile.Name, "installed profile: "+actualProfile, fmt.Sprintf("installed profile is %s; expected %s", defaultString(actualProfile, "unknown"), profile.Name))
		expectedRevision := strconv.Itoa(profile.ProfileRevision)
		if actualRevision == expectedRevision {
			fmt.Fprintf(a.out, "[ok]   installed profile revision: %s\n", actualRevision)
		} else if actualRevision == "" && profile.ProfileRevision == 1 {
			fmt.Fprintf(a.out, "[warn] installed profile revision: 1 (legacy marker inferred; run --sync %s)\n", profile.Name)
		} else {
			fmt.Fprintf(a.out, "[fail] installed profile revision is %s; available revision is %s\n", defaultString(actualRevision, "missing"), expectedRevision)
			problems++
		}
		check(actualNode == a.node, "Proxmox node label: "+actualNode, fmt.Sprintf("Proxmox node label is %s; current node is %s (run --migrate)", defaultString(actualNode, "missing"), a.node))
		check(bytes.Equal(data, generated), "deployed configuration matches generated configuration", "deployed configuration has drifted")
	} else {
		fmt.Fprintf(a.out, "[fail] managed configuration is not installed: %s\n", a.site.RemoteConfig)
		problems++
	}

	for _, legacy := range a.site.LegacyConfigs {
		if a.remoteFileExists(ctid, legacy) {
			fmt.Fprintf(a.out, "[fail] legacy configuration is still active: %s\n", legacy)
			problems++
		} else if a.patternExists(ctid, legacy+".disabled.*") {
			fmt.Fprintf(a.out, "[ok]   legacy configuration is disabled and preserved: %s\n", legacy)
		} else {
			fmt.Fprintf(a.out, "[ok]   legacy configuration is not present: %s\n", legacy)
		}
	}
	_, err = a.pct.Exec(ctid, "bash", "-c", `exec 3<>/dev/tcp/$1/$2`, "_", a.site.Alloy.Host, strconv.Itoa(a.site.Alloy.Port))
	check(err == nil, fmt.Sprintf("Alloy is reachable at %s:%d/tcp", a.site.Alloy.Host, a.site.Alloy.Port), fmt.Sprintf("Alloy is not reachable at %s:%d/tcp", a.site.Alloy.Host, a.site.Alloy.Port))

	for _, source := range append(append([]Source{}, profile.Files...), profile.Tasks...) {
		if a.patternExists(ctid, source.Path) {
			fmt.Fprintf(a.out, "[ok]   source exists: %s (%s)\n", source.Path, source.Service)
		} else if source.Required {
			fmt.Fprintf(a.out, "[fail] required source missing: %s (%s)\n", source.Path, source.Service)
			problems++
		} else {
			fmt.Fprintf(a.out, "[warn] optional source absent: %s (%s)\n", source.Path, source.Service)
		}
	}
	if profile.Docker.Enabled {
		if profile.Docker.mode() == "api" {
			fmt.Fprintf(a.out, "[ok]   Docker API discovery: v%s, polling every %ds\n", docker.APIVersion, profile.Docker.PollingInterval)
		} else if len(docker.Sources) > 0 {
			fmt.Fprintf(a.out, "[ok]   Docker log mappings: %d\n", len(docker.Sources))
		} else {
			fmt.Fprintln(a.out, "[warn] no Docker json-file log paths were discovered")
		}
	}
	a.printNotes(profile)
	fmt.Fprintf(a.out, "\nResult: %d problem(s)\n", problems)
	if problems > 0 {
		return fmt.Errorf("status found %d problem(s)", problems)
	}
	return nil
}

func (a *app) printNotes(profile Profile) {
	if len(profile.Notes) == 0 {
		return
	}
	fmt.Fprintln(a.out, "\nProfile notes:")
	for _, note := range profile.Notes {
		fmt.Fprintf(a.out, "  - %s\n", note)
	}
}

func (a *app) listProfiles() {
	fmt.Fprintf(a.out, "%-20s %-9s %-8s %s\n%s\n", "PROFILE", "REVISION", "MODE", "DESCRIPTION", strings.Repeat("-", 64))
	for _, profile := range a.profiles {
		mode := "journal"
		if profile.Docker.Enabled {
			mode = "docker"
		}
		if len(profile.Files) > 0 {
			mode = "files"
		}
		if len(profile.Tasks) > 0 {
			mode = "tasks"
		}
		fmt.Fprintf(a.out, "%-20s %-9d %-8s %s\n", profile.Name, profile.ProfileRevision, mode, profile.Description)
	}
}

func (a *app) reconcile(mode, filter string) error {
	if filter != "" {
		if _, err := a.profile(filter); err != nil {
			return err
		}
	}
	containers, err := a.pct.List()
	if err != nil {
		return err
	}
	managed, current, updated, previewed, failed, deferred, legacy := 0, 0, 0, 0, 0, 0, 0
	if mode == "inventory" {
		fmt.Fprintf(a.out, "%-7s %-20s %-12s %-12s %s\n%s\n", "CTID", "PROFILE", "INSTALLED", "AVAILABLE", "RESULT", strings.Repeat("-", 64))
	}
	for _, container := range containers {
		if container.Status != "running" {
			deferred++
			if mode == "inventory" {
				fmt.Fprintf(a.out, "%-7d %-20s %-12s %-12s %s\n", container.ID, "unknown", "unknown", "unknown", "stopped; not inspected")
			} else {
				a.warn("CT %d is stopped and could not be inspected", container.ID)
			}
			continue
		}
		metadata, metadataErr := a.installedMetadata(container.ID)
		if metadataErr != nil || metadata["profile"] == "" || filter != "" && metadata["profile"] != filter {
			continue
		}
		managed++
		profile, ok := a.profileByName[metadata["profile"]]
		if !ok {
			failed++
			if mode == "inventory" {
				fmt.Fprintf(a.out, "%-7d %-20s %-12s %-12s %s\n", container.ID, metadata["profile"], defaultString(metadata["revision"], "unknown"), "unknown", "local profile missing or invalid")
			} else {
				a.warn("CT %d references unavailable profile %q", container.ID, metadata["profile"])
			}
			continue
		}
		installedRevision := metadata["revision"]
		revisionLabel := installedRevision
		revisionNumber := 0
		if installedRevision == "" {
			revisionNumber = 1
			revisionLabel = "1 (legacy)"
			legacy++
		} else {
			revisionNumber, _ = strconv.Atoi(installedRevision)
			if revisionNumber < 1 {
				revisionLabel = "invalid"
			}
		}
		result := "current"
		switch {
		case revisionLabel == "invalid":
			result = "invalid installed revision"
		case strings.Contains(revisionLabel, "legacy"):
			result = "legacy metadata; sync will stamp revision"
		case revisionNumber < profile.ProfileRevision:
			result = "update available"
		case revisionNumber > profile.ProfileRevision:
			result = "local profile is older"
		case metadata["profile_hash"] != profile.Hash:
			result = "hash drift at same revision"
		case metadata["node"] != a.node:
			result = "node label needs refresh"
		default:
			current++
		}
		if mode == "inventory" {
			fmt.Fprintf(a.out, "%-7d %-20s %-12s %-12d %s\n", container.ID, profile.Name, revisionLabel, profile.ProfileRevision, result)
			continue
		}
		a.info("Reconciling CT %d with profile %s (installed: %s, available: %d)", container.ID, profile.Name, revisionLabel, profile.ProfileRevision)
		deployResult, installErr := a.install(container.ID, profile.Name)
		if installErr != nil {
			fmt.Fprintln(a.errOut, installErr)
			failed++
		} else if a.dryRun {
			previewed++
		} else if deployResult.Current {
			current++
		} else {
			updated++
		}
		fmt.Fprintln(a.out, strings.Repeat("-", 64))
	}
	if mode == "inventory" {
		fmt.Fprintf(a.out, "\nManaged: %d  Current: %d  Legacy: %d  Deferred stopped: %d  Problems: %d\n", managed, current, legacy, deferred, failed)
	} else {
		fmt.Fprintf(a.out, "\nSync summary: matched=%d updated=%d current=%d previewed=%d failed=%d stopped-uninspected=%d\n", managed, updated, current, previewed, failed, deferred)
	}
	if failed > 0 {
		return fmt.Errorf("%d operation(s) failed", failed)
	}
	return nil
}
