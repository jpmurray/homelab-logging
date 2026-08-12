package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var packageItems = []string{
	"VERSION", "README.md", "CHANGELOG.md", "LICENSE", "config.json",
	"alloy", "docs", "grafana", "schema", "services",
}

func main() {
	versionBytes, err := os.ReadFile("VERSION")
	must(err)
	version := strings.TrimSpace(string(versionBytes))
	name := "homelab-logging-" + version
	stage, err := os.MkdirTemp("", "homelab-logging-package-*")
	must(err)
	defer os.RemoveAll(stage)

	binary := filepath.Join(stage, "homelab-logging")
	command := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, ".")
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	must(command.Run())

	must(os.MkdirAll("dist", 0755))
	output := filepath.Join("dist", name+"-linux-amd64.zip")
	file, err := os.Create(output)
	must(err)
	archive := zip.NewWriter(file)
	must(addFile(archive, binary, filepath.Join(name, "homelab-logging")))
	for _, item := range packageItems {
		must(addPath(archive, item, filepath.Join(name, item)))
	}
	must(archive.Close())
	must(file.Close())
	fmt.Println(output)
}

func addPath(archive *zip.Writer, source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return addFile(archive, source, target)
	}
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		return addFile(archive, path, filepath.Join(target, relative))
	})
}

func addFile(archive *zip.Writer, source, target string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(target)
	header.Method = zip.Deflate
	if filepath.Base(target) == "homelab-logging" {
		header.SetMode(0755)
	}
	writer, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(writer, file)
	return err
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "package:", err)
		os.Exit(1)
	}
}
