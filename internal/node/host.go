package node

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Info is the host identity kubectl prints for a node.
type Info struct {
	OperatingSystem         string
	Architecture            string
	OSImage                 string
	KernelVersion           string
	KubeletVersion          string
	ContainerRuntimeVersion string
	// GPUShares is how many apple.com/gpu shares this heartbeat advertises.
	// Zero means the scheduler will not place a GPU pod.
	GPUShares int64
}

// Detect reads the Mac's OS, kernel, and Apple container CLI version.
// kubeletVersion is the Kubernetes version this agent is standing in for.
func Detect(kubeletVersion, containerBin string) Info {
	if containerBin == "" {
		containerBin = "container"
	}
	return Info{
		OperatingSystem:         "darwin",
		Architecture:            parseArch(command("uname", "-m")),
		OSImage:                 parseSWVers(command("sw_vers")),
		KernelVersion:           strings.TrimSpace(command("uname", "-r")),
		KubeletVersion:          kubeletVersion,
		ContainerRuntimeVersion: parseContainerVersion(command(containerBin, "--version")),
	}
}

// Name is this machine's short hostname, sanitized into a node name.
func Name() (string, error) {
	host, err := os.Hostname()
	if err != nil {
		return "", err
	}
	name := Sanitize(host)
	if name == "" {
		return "", fmt.Errorf("hostname %q is not a usable node name", host)
	}
	return name, nil
}

// Sanitize turns a hostname into a lowercase DNS label.
func Sanitize(host string) string {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func command(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return string(out)
}

func parseSWVers(out string) string {
	vals := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		vals[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	name, ver, build := vals["ProductName"], vals["ProductVersion"], vals["BuildVersion"]
	switch {
	case name != "" && ver != "" && build != "":
		return fmt.Sprintf("%s %s (%s)", name, ver, build)
	case name != "" && ver != "":
		return name + " " + ver
	default:
		return ""
	}
}

func parseArch(out string) string {
	switch strings.TrimSpace(out) {
	case "x86_64":
		return "amd64"
	case "aarch64", "arm64", "":
		return "arm64"
	default:
		return strings.TrimSpace(out)
	}
}

func parseContainerVersion(out string) string {
	fields := strings.Fields(out)
	for i, field := range fields {
		if strings.EqualFold(field, "version") && i+1 < len(fields) {
			ver := strings.Trim(fields[i+1], ",")
			if ver != "" {
				return "container://" + ver
			}
		}
	}
	return "container://unknown"
}
