package main

import (
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const launchAgentLabel = "com.tocy.watch"

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist"), nil
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.Exe}}</string>
		<string>watch</string>
		<string>--interval</string>
		<string>{{.Interval}}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath}}</string>
</dict>
</plist>
`

func installLaunchAgent(interval string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchd install is only supported on macOS")
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve tocy binary path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve tocy binary path: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	tocyDir := filepath.Join(home, ".tocy")
	if err := os.MkdirAll(tocyDir, 0o700); err != nil {
		return err
	}

	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	tmpl := template.Must(template.New("plist").Parse(plistTemplate))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	data := struct{ Label, Exe, Interval, LogPath string }{
		Label:    launchAgentLabel,
		Exe:      exe,
		Interval: interval,
		LogPath:  filepath.Join(tocyDir, "watch.log"),
	}
	if err := tmpl.Execute(f, data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}

	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, path).Run()
	out, err := exec.Command("launchctl", "bootstrap", uid, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %w\n%s", err, out)
	}
	fmt.Printf("installed launchd agent: %s\n", path)
	fmt.Printf("logs: %s\n", data.LogPath)
	return nil
}

func uninstallLaunchAgent() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launchd install is only supported on macOS")
	}
	path, err := launchAgentPath()
	if err != nil {
		return err
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := exec.Command("launchctl", "bootout", uid, path).CombinedOutput()
	if err != nil {
		fmt.Printf("launchctl bootout: %v\n%s\n", err, out)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("removed launchd agent: %s\n", path)
	return nil
}
