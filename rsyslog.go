package main

import (
	"fmt"
	"sort"
	"strings"
)

type dockerSource struct {
	Service string
	Path    string
}

type dockerRuntime struct {
	Sources    []dockerSource
	APIVersion string
}

func renderRsyslog(site SiteConfig, siteHash string, profile Profile, docker dockerRuntime, node string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by homelab-logging v%s. Local edits will be replaced.\n", version)
	fmt.Fprintf(&b, "# homelab-logging-profile: %s\n", profile.Name)
	fmt.Fprintf(&b, "# homelab-logging-profile-revision: %d\n", profile.ProfileRevision)
	fmt.Fprintf(&b, "# homelab-logging-node: %s\n", node)
	fmt.Fprintf(&b, "# homelab-logging-profile-sha256: %s\n", profile.Hash)
	fmt.Fprintf(&b, "# homelab-logging-site-sha256: %s\n\n", siteHash)

	hasFiles := len(profile.Files) > 0 || len(profile.Tasks) > 0
	hasDockerFiles := profile.Docker.Enabled && profile.Docker.mode() == "files" && len(docker.Sources) > 0
	hasDockerAPI := profile.Docker.Enabled && profile.Docker.mode() == "api"
	if hasFiles || hasDockerFiles {
		b.WriteString("module(load=\"imfile\" PollingInterval=\"10\")\n\n")
	}
	if hasDockerAPI {
		emitDockerAPIModule(&b, profile.Docker, docker.APIVersion)
	}

	emitForwardTemplate(&b, "AlloySyslogForward", "syslog", site, node)
	if hasDockerFiles {
		emitForwardTemplate(&b, "AlloyDockerForward", "docker", site, node)
	} else if hasDockerAPI {
		emitDockerForwardTemplate(&b, site, node)
	}
	hasTaskMetadata := false
	for _, task := range profile.Tasks {
		hasTaskMetadata = hasTaskMetadata || task.IncludeFilename
	}
	if hasTaskMetadata {
		emitTaskTemplate(&b, site, node)
	}
	if hasFiles {
		emitRuleset(&b, "alloy_files", site, "AlloySyslogForward", "alloy-files")
	}
	if hasTaskMetadata {
		emitTaskRuleset(&b, site)
	}
	if hasDockerFiles {
		emitRuleset(&b, "alloy_docker", site, "AlloyDockerForward", "alloy-docker")
	} else if hasDockerAPI {
		emitDockerAPIRoute(&b, site)
	}

	for _, source := range profile.Files {
		emitInput(&b, source, "alloy_files", false)
	}
	for _, source := range profile.Tasks {
		ruleset := "alloy_files"
		if source.IncludeFilename {
			ruleset = "alloy_tasks"
		}
		emitInput(&b, source, ruleset, source.IncludeFilename)
	}
	sort.Slice(docker.Sources, func(i, j int) bool {
		if docker.Sources[i].Service == docker.Sources[j].Service {
			return docker.Sources[i].Path < docker.Sources[j].Path
		}
		return docker.Sources[i].Service < docker.Sources[j].Service
	})
	for _, source := range docker.Sources {
		emitInput(&b, Source{Path: source.Path, Service: source.Service, Facility: "local6", Severity: "info"}, "alloy_docker", false)
	}
	if profile.Journal {
		b.WriteString("# Forward messages received through the container syslog/journal path.\n")
		emitForwardAction(&b, site, "AlloySyslogForward", "alloy-journal")
	}
	return b.String()
}

func emitDockerAPIModule(b *strings.Builder, docker Docker, apiVersion string) {
	fmt.Fprintf(b, `module(
    load="imdocker"
    DockerApiUnixSockAddr="/var/run/docker.sock"
    ApiVersionStr="v%s"
    PollingInterval="%d"
    GetContainerLogOptions="timestamps=0&follow=1&stdout=1&stderr=1&tail=1"
    RetrieveNewLogsFromStart="on"
    DefaultFacility="local6"
    DefaultSeverity="info"
)

`, apiVersion, docker.PollingInterval)
}

func emitForwardTemplate(b *strings.Builder, name, job string, site SiteConfig, node string) {
	fmt.Fprintf(b, `template(name="%s" type="list") {
    constant(value="<")
    property(name="pri")
    constant(value=">1 ")
    property(name="timereported" dateFormat="rfc3339")
    constant(value=" ")
    property(name="hostname")
    constant(value=" ")
    property(name="app-name" position.from="1" position.to="48")
    constant(value=" - - [homelab@32473 cluster=\"%s\" location=\"%s\" role=\"%s\" node=\"%s\" job=\"%s\"] ")
    property(name="msg" droplastlf="on")
    constant(value="\n")
}

`, name, site.Cluster, site.Location, site.OriginRole, node, job)
}

func emitDockerForwardTemplate(b *strings.Builder, site SiteConfig, node string) {
	fmt.Fprintf(b, `template(name="AlloyDockerForward" type="list") {
    constant(value="<")
    property(name="pri")
    constant(value=">1 ")
    property(name="timereported" dateFormat="rfc3339")
    constant(value=" ")
    property(name="hostname")
    constant(value=" ")
    property(name="$.docker_service" position.from="1" position.to="48")
    constant(value=" - - [homelab@32473 cluster=\"%s\" location=\"%s\" role=\"%s\" node=\"%s\" job=\"docker\"] ")
    property(name="msg" droplastlf="on")
    constant(value="\n")
}

`, site.Cluster, site.Location, site.OriginRole, node)
}

func emitTaskTemplate(b *strings.Builder, site SiteConfig, node string) {
	fmt.Fprintf(b, `template(name="AlloyTaskForward" type="list") {
    constant(value="<")
    property(name="pri")
    constant(value=">1 ")
    property(name="timereported" dateFormat="rfc3339")
    constant(value=" ")
    property(name="hostname")
    constant(value=" ")
    property(name="app-name" position.from="1" position.to="48")
    constant(value=" - - [homelab@32473 cluster=\"%s\" location=\"%s\" role=\"%s\" node=\"%s\" job=\"syslog\"] [")
    property(name="$.task_id")
    constant(value="] ")
    property(name="msg" droplastlf="on")
    constant(value="\n")
}

`, site.Cluster, site.Location, site.OriginRole, node)
}

func emitForwardAction(b *strings.Builder, site SiteConfig, template, queue string) {
	fmt.Fprintf(b, `    action(
        type="omfwd"
        target="%s"
        port="%d"
        protocol="tcp"
        template="%s"
        queue.type="linkedList"
        queue.filename="%s"
        queue.saveOnShutdown="on"
        action.resumeRetryCount="-1"
    )
`, site.Alloy.Host, site.Alloy.Port, template, queue)
}

func emitRuleset(b *strings.Builder, name string, site SiteConfig, template, queue string) {
	fmt.Fprintf(b, "ruleset(name=\"%s\") {\n", name)
	emitForwardAction(b, site, template, queue)
	b.WriteString("    stop\n}\n\n")
}

func emitDockerAPIRoute(b *strings.Builder, site SiteConfig) {
	b.WriteString(`if ($inputname == "imdocker") then {
    set $.docker_service = re_extract($!metadata!Names, "^/?([A-Za-z0-9][A-Za-z0-9_.-]{0,63})\$", 0, 1, "docker");
`)
	emitForwardAction(b, site, "AlloyDockerForward", "alloy-docker")
	b.WriteString("    stop\n}\n\n")
}

func emitTaskRuleset(b *strings.Builder, site SiteConfig) {
	b.WriteString(`ruleset(name="alloy_tasks") {
    set $.task_id = re_extract($!metadata!filename, "([^/]+)\$", 0, 1, "unknown-task");
`)
	emitForwardAction(b, site, "AlloyTaskForward", "alloy-tasks")
	b.WriteString("    stop\n}\n\n")
}

func emitInput(b *strings.Builder, source Source, ruleset string, metadata bool) {
	fmt.Fprintf(b, `input(
    type="imfile"
    File="%s"
    Tag="%s:"
    Facility="%s"
    Severity="%s"
    PersistStateInterval="100"
    freshStartTail="on"
    addMetadata="%s"
    Ruleset="%s"
)

`, source.Path, source.Service, defaultString(source.Facility, "local5"), defaultString(source.Severity, "info"), onOff(metadata), ruleset)
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}
