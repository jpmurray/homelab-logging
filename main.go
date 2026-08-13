package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const version = "1.4.1"

const hostTargetID = 1

type options struct {
	action      string
	configPath  string
	profilesDir string
	dryRun      bool
	host        bool
	ctid        int
	profile     string
}

func usage() {
	fmt.Print(`homelab-logging - deploy declarative rsyslog forwarding on Proxmox hosts and LXCs

Usage:
  homelab-logging <CTID> [PROFILE] [--dry-run]
  homelab-logging <CTID> [PROFILE] --status
  homelab-logging <CTID> --migrate [--dry-run]
  homelab-logging --host [PROFILE] [--dry-run]
  homelab-logging --host [PROFILE] --status
  homelab-logging --host --inventory
  homelab-logging --host --sync [PROFILE] [--dry-run]
  homelab-logging --inventory [PROFILE]
  homelab-logging --sync [PROFILE] [--dry-run]
  homelab-logging --list
  homelab-logging --validate [PROFILE]

Options:
  --config PATH         Site configuration (default: ./config.json)
  --profiles-dir PATH   Profile directory (default: ./services)
  --host                Manage logging on the local Proxmox VE host
  --dry-run             Generate and display changes without modifying the target
  --status              Audit installation, service, destination, and sources
  --migrate             Refresh an installed profile after moving an LXC
  --inventory           Show installed and available profile revisions
  --sync                Reconcile previously managed targets
  --list                List available profiles
  --validate            Validate site configuration and profiles
  --version             Print the version
  -h, --help            Show this help
`)
}

func defaultRoot() string {
	cwd, _ := os.Getwd()
	if _, err := os.Stat(filepath.Join(cwd, "config.json")); err == nil {
		return cwd
	}
	executable, err := os.Executable()
	if err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		return filepath.Dir(executable)
	}
	return cwd
}

func parseOptions(args []string) (options, error) {
	root := defaultRoot()
	opts := options{action: "install", configPath: filepath.Join(root, "config.json"), profilesDir: filepath.Join(root, "services")}
	for i := 0; i < len(args); {
		arg := args[i]
		switch arg {
		case "--config", "--profiles-dir":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a path", arg)
			}
			if arg == "--config" {
				opts.configPath = args[i+1]
			} else {
				opts.profilesDir = args[i+1]
			}
			i += 2
		case "--dry-run":
			opts.dryRun = true
			i++
		case "--host":
			opts.host = true
			i++
		case "--status":
			opts.action = "status"
			i++
		case "--migrate":
			opts.action = "migrate"
			i++
		case "--inventory":
			opts.action = "inventory"
			i++
		case "--sync":
			opts.action = "sync"
			i++
		case "--list":
			opts.action = "list"
			i++
		case "--validate":
			opts.action = "validate"
			i++
		case "--version":
			opts.action = "version"
			i++
		case "-h", "--help":
			opts.action = "help"
			i++
		default:
			if strings.HasPrefix(arg, "--") {
				return opts, fmt.Errorf("unknown option: %s", arg)
			}
			if id, err := strconv.Atoi(arg); err == nil && opts.ctid == 0 {
				opts.ctid = id
			} else if opts.profile == "" {
				opts.profile = arg
			} else {
				return opts, fmt.Errorf("unexpected argument: %s", arg)
			}
			i++
		}
	}
	return opts, nil
}

func nodeName() (string, error) {
	node := os.Getenv("HLL_NODE_OVERRIDE")
	if node == "" {
		host, err := os.Hostname()
		if err != nil {
			return "", err
		}
		node = strings.SplitN(host, ".", 2)[0]
	}
	if !safeName.MatchString(node) {
		return "", fmt.Errorf("invalid Proxmox node name: %s", node)
	}
	return node, nil
}

func run(args []string) error {
	opts, err := parseOptions(args)
	if err != nil {
		return err
	}
	switch opts.action {
	case "help":
		usage()
		return nil
	case "version":
		fmt.Println(version)
		return nil
	}

	site, siteHash, err := loadSite(opts.configPath)
	if err != nil {
		return err
	}
	profiles, err := loadProfiles(opts.profilesDir)
	if err != nil {
		return err
	}
	if opts.action == "validate" {
		if opts.profile != "" {
			found := false
			for _, profile := range profiles {
				found = found || profile.Name == opts.profile
			}
			if !found {
				return fmt.Errorf("unknown profile %q", opts.profile)
			}
			fmt.Printf("==> Site configuration and profile %q are valid\n", opts.profile)
		} else {
			fmt.Println("==> Site configuration and all profiles are valid")
		}
		return nil
	}

	node, err := nodeName()
	if err != nil {
		return err
	}
	if opts.action == "list" {
		application := newApp(site, siteHash, profiles, nil, node, os.Stdout, os.Stderr)
		application.listProfiles()
		return nil
	}
	if opts.host {
		if opts.ctid != 0 {
			return fmt.Errorf("--host does not accept a CTID")
		}
		if opts.action == "migrate" {
			return fmt.Errorf("--migrate applies only to LXCs; host labels always use the local node name")
		}
		if !opts.dryRun && contains([]string{"install", "sync"}, opts.action) && os.Geteuid() != 0 && os.Getenv("HLL_TESTING") != "1" {
			return fmt.Errorf("run this command as root on a Proxmox VE host")
		}
		hostSite := site
		hostSite.OriginRole = "proxmox-host"
		hostSiteHash := hashBytes([]byte(siteHash + "\norigin_role=proxmox-host"))
		application := newApp(hostSite, hostSiteHash, profiles, newLocalClient(), node, os.Stdout, os.Stderr)
		application.dryRun = opts.dryRun
		application.host = true
		switch opts.action {
		case "inventory":
			if opts.dryRun {
				return fmt.Errorf("--dry-run is only valid with --sync")
			}
			return application.reconcile("inventory", opts.profile)
		case "sync":
			return application.reconcile("sync", opts.profile)
		case "status":
			return application.status(hostTargetID, opts.profile)
		case "install":
			_, err := application.install(hostTargetID, opts.profile)
			return err
		default:
			return fmt.Errorf("action %q is not valid with --host", opts.action)
		}
	}

	pctBinary := os.Getenv("PCT_BIN")
	if pctBinary == "" {
		pctBinary = "pct"
	}
	client := newPCTClient(pctBinary)
	application := newApp(site, siteHash, profiles, client, node, os.Stdout, os.Stderr)
	application.dryRun = opts.dryRun
	if err := requirePCT(pctBinary); err != nil {
		return err
	}

	switch opts.action {
	case "inventory":
		if opts.ctid != 0 {
			return fmt.Errorf("--inventory does not accept a CTID")
		}
		if opts.dryRun {
			return fmt.Errorf("--dry-run is only valid with --sync")
		}
		return application.reconcile("inventory", opts.profile)
	case "sync":
		if opts.ctid != 0 {
			return fmt.Errorf("--sync does not accept a CTID")
		}
		if !opts.dryRun && os.Geteuid() != 0 && os.Getenv("HLL_TESTING") != "1" {
			return fmt.Errorf("run this command as root on a Proxmox VE host")
		}
		return application.reconcile("sync", opts.profile)
	case "status":
		if opts.ctid == 0 {
			return fmt.Errorf("a CTID is required")
		}
		return application.status(opts.ctid, opts.profile)
	case "migrate":
		if opts.ctid == 0 {
			return fmt.Errorf("a CTID is required")
		}
		if opts.profile != "" {
			return fmt.Errorf("--migrate uses the installed profile; do not specify a profile")
		}
		if !opts.dryRun && os.Geteuid() != 0 && os.Getenv("HLL_TESTING") != "1" {
			return fmt.Errorf("run this command as root on a Proxmox VE host")
		}
		return application.migrate(opts.ctid)
	case "install":
		if opts.ctid == 0 {
			return fmt.Errorf("a CTID is required")
		}
		if !opts.dryRun && os.Geteuid() != 0 && os.Getenv("HLL_TESTING") != "1" {
			return fmt.Errorf("run this command as root on a Proxmox VE host")
		}
		_, err := application.install(opts.ctid, opts.profile)
		return err
	default:
		return fmt.Errorf("unknown action %q", opts.action)
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
