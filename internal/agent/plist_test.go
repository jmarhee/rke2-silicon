package agent

import (
	"strings"
	"testing"
)

func TestPlistContainsProgram(t *testing.T) {
	got := string(Plist("/usr/local/bin/rke2-silicon", "/Users/me/.rke2-silicon", "/Users/me/.rke2-silicon/agent.log"))
	for _, want := range []string{
		"<key>Label</key><string>com.rke2-silicon.agent</string>",
		"<string>/usr/local/bin/rke2-silicon</string>",
		"<string>agent</string>",
		"<string>--data-dir</string>",
		"<string>/Users/me/.rke2-silicon</string>",
		"<key>KeepAlive</key><true/>",
		"<key>RunAtLoad</key><true/>",
		"<key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %s\n%s", want, got)
		}
	}
}

func TestPlistEscapesXML(t *testing.T) {
	got := string(Plist("/tmp/a&b", "/tmp/x<y", "/tmp/log"))
	if strings.Contains(got, "/tmp/a&b") || !strings.Contains(got, "/tmp/a&amp;b") {
		t.Fatalf("plist did not escape the executable: %s", got)
	}
}
