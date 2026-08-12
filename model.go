package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type SiteConfig struct {
	SchemaVersion int      `json:"schema_version"`
	Cluster       string   `json:"cluster"`
	Location      string   `json:"location"`
	OriginRole    string   `json:"origin_role"`
	Alloy         Alloy    `json:"alloy"`
	RemoteConfig  string   `json:"remote_config"`
	LegacyConfigs []string `json:"legacy_configs"`
}

type Alloy struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

type Profile struct {
	SchemaVersion   int       `json:"schema_version"`
	ProfileRevision int       `json:"profile_revision"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Journal         bool      `json:"journal"`
	RequiredPaths   []string  `json:"required_paths"`
	Files           []Source  `json:"files"`
	Tasks           []Source  `json:"tasks"`
	Docker          Docker    `json:"docker"`
	Detect          Detection `json:"detect"`
	TestService     string    `json:"test_service"`
	Notes           []string  `json:"notes"`
	Path            string    `json:"-"`
	Hash            string    `json:"-"`
}

type Source struct {
	Path            string `json:"path"`
	Service         string `json:"service"`
	Required        bool   `json:"required,omitempty"`
	Facility        string `json:"facility,omitempty"`
	Severity        string `json:"severity,omitempty"`
	IncludeFilename bool   `json:"include_filename,omitempty"`
}

type Docker struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode,omitempty"`
	PollingInterval int    `json:"polling_interval,omitempty"`
}

func (d Docker) mode() string {
	if d.Mode == "" {
		return "files"
	}
	return d.Mode
}

type Detection struct {
	Mode     string  `json:"mode"`
	Priority int     `json:"priority"`
	Probes   []Probe `json:"probes"`
}

type Probe struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

var (
	safeName        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	safeHost        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,252}$`)
	rsyslogPath     = regexp.MustCompile(`^/etc/rsyslog[.]d/[A-Za-z0-9._-]+[.]conf$`)
	allowedFacility = regexp.MustCompile(`^(auth|authpriv|cron|daemon|kern|local[0-7]|mail|news|syslog|user|uucp)$`)
	allowedSeverity = regexp.MustCompile(`^(debug|info|notice|warning|err|crit|alert|emerg)$`)
)

func decodeStrict(path string, target any) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return data, nil
}

func loadSite(path string) (SiteConfig, string, error) {
	var site SiteConfig
	data, err := decodeStrict(path, &site)
	if err != nil {
		return site, "", fmt.Errorf("read site configuration: %w", err)
	}
	if err := site.validate(); err != nil {
		return site, "", fmt.Errorf("invalid site configuration: %w", err)
	}
	return site, hashBytes(data), nil
}

func (s SiteConfig) validate() error {
	if s.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1")
	}
	for label, value := range map[string]string{"cluster": s.Cluster, "location": s.Location, "origin_role": s.OriginRole} {
		if !safeName.MatchString(value) {
			return fmt.Errorf("%s is invalid", label)
		}
	}
	if !safeHost.MatchString(s.Alloy.Host) || s.Alloy.Port < 1 || s.Alloy.Port > 65535 || s.Alloy.Protocol != "tcp" {
		return fmt.Errorf("alloy destination must be a valid TCP host and port")
	}
	if !rsyslogPath.MatchString(s.RemoteConfig) {
		return fmt.Errorf("remote_config must be an absolute .conf path in /etc/rsyslog.d")
	}
	seen := map[string]bool{s.RemoteConfig: true}
	for _, path := range s.LegacyConfigs {
		if !rsyslogPath.MatchString(path) || seen[path] {
			return fmt.Errorf("invalid or duplicate legacy configuration %q", path)
		}
		seen[path] = true
	}
	return nil
}

func loadProfile(path string) (Profile, error) {
	var p Profile
	data, err := decodeStrict(path, &p)
	if err != nil {
		return p, err
	}
	p.Path = path
	p.Hash = hashBytes(data)
	if err := p.validate(); err != nil {
		return p, err
	}
	if strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) != p.Name {
		return p, fmt.Errorf("profile name must match filename")
	}
	return p, nil
}

func (p Profile) validate() error {
	if p.SchemaVersion != 1 || p.ProfileRevision < 1 {
		return fmt.Errorf("invalid schema or profile revision")
	}
	if !safeName.MatchString(p.Name) || !safeName.MatchString(p.TestService) || p.Description == "" {
		return fmt.Errorf("invalid profile identity")
	}
	if p.Detect.Mode != "all" && p.Detect.Mode != "any" {
		return fmt.Errorf("detect.mode must be all or any")
	}
	if p.Detect.Priority < 0 || p.Detect.Priority > 1000 {
		return fmt.Errorf("detect.priority is out of range")
	}
	for _, path := range p.RequiredPaths {
		if !safeAbsolutePath(path) {
			return fmt.Errorf("unsafe required path %q", path)
		}
	}
	for _, source := range append(append([]Source{}, p.Files...), p.Tasks...) {
		if !safeAbsolutePath(source.Path) || !safeName.MatchString(source.Service) {
			return fmt.Errorf("invalid source %q", source.Path)
		}
		facility := defaultString(source.Facility, "local5")
		severity := defaultString(source.Severity, "info")
		if !allowedFacility.MatchString(facility) || !allowedSeverity.MatchString(severity) {
			return fmt.Errorf("invalid facility or severity for %q", source.Path)
		}
	}
	for _, probe := range p.Detect.Probes {
		if !contains([]string{"path", "service", "command", "package"}, probe.Type) || probe.Value == "" || len(probe.Value) > 255 || strings.Contains(probe.Value, "\n") {
			return fmt.Errorf("invalid detection probe")
		}
	}
	if !p.Docker.Enabled {
		if p.Docker.Mode != "" || p.Docker.PollingInterval != 0 {
			return fmt.Errorf("docker mode options require docker.enabled")
		}
	} else {
		switch p.Docker.mode() {
		case "files":
			if p.Docker.PollingInterval != 0 {
				return fmt.Errorf("docker.polling_interval is only valid in api mode")
			}
		case "api":
			if p.Docker.PollingInterval < 1 || p.Docker.PollingInterval > 3600 {
				return fmt.Errorf("docker.polling_interval must be between 1 and 3600 in api mode")
			}
		default:
			return fmt.Errorf("docker.mode must be files or api")
		}
	}
	if !p.Journal && len(p.Files) == 0 && len(p.Tasks) == 0 && !p.Docker.Enabled {
		return fmt.Errorf("profile has no log source")
	}
	return nil
}

func loadProfiles(directory string) ([]Profile, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, fmt.Errorf("no profiles found in %s", directory)
	}
	profiles := make([]Profile, 0, len(paths))
	for _, path := range paths {
		p, err := loadProfile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

func safeAbsolutePath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.ContainsAny(path, "\n\"")
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
