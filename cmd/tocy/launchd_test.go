package main

import (
	"strings"
	"testing"
	"text/template"
)

func TestPlistTemplateRenders(t *testing.T) {
	tmpl := template.Must(template.New("plist").Parse(plistTemplate))
	var b strings.Builder
	data := struct{ Label, Exe, Interval, LogPath string }{
		Label:    launchAgentLabel,
		Exe:      "/usr/local/bin/tocy",
		Interval: "5m",
		LogPath:  "/Users/me/.tocy/watch.log",
	}
	if err := tmpl.Execute(&b, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"<string>com.tocy.watch</string>",
		"<string>/usr/local/bin/tocy</string>",
		"<string>watch</string>",
		"<string>--interval</string>",
		"<string>5m</string>",
		"<string>/Users/me/.tocy/watch.log</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist missing %q\n%s", want, out)
		}
	}
}
