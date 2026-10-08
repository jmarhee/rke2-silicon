package node

import "testing"

func TestParseSWVers(t *testing.T) {
	out := "ProductName:\tmacOS\nProductVersion:\t26.5.1\nBuildVersion:\t25F80\n"
	if got := parseSWVers(out); got != "macOS 26.5.1 (25F80)" {
		t.Fatalf("os image %q", got)
	}
}

func TestParseArch(t *testing.T) {
	if got := parseArch("arm64\n"); got != "arm64" {
		t.Fatalf("arch %q", got)
	}
	if got := parseArch("x86_64\n"); got != "amd64" {
		t.Fatalf("arch %q", got)
	}
}

func TestParseContainerVersion(t *testing.T) {
	out := "container CLI version 1.4.1 (build: release, commit: 9a8917c)\n"
	if got := parseContainerVersion(out); got != "container://1.4.1" {
		t.Fatalf("runtime %q", got)
	}
	if got := parseContainerVersion(""); got != "container://unknown" {
		t.Fatalf("runtime %q", got)
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("Dreamland.local"); got != "dreamland" {
		t.Fatalf("name %q", got)
	}
}
