// Package catalog maps a Kubernetes version to the container images that run it.
package catalog

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	// DistroRKE2 runs etcd, kube-apiserver, kube-controller-manager, and
	// kube-scheduler as separate containers.
	DistroRKE2 = "rke2"
	// DistroK3s runs one k3s server container. The datastore is sqlite or a
	// single-member embedded etcd.
	DistroK3s = "k3s"
)

// Release is one supported Kubernetes version.
type Release struct {
	Version         string
	Distro          string
	KubernetesImage string
	EtcdImage       string
}

// releases is the stand-in for kontainer-driver-metadata.
var releases = []Release{
	{
		Version:         "v1.36.4+rke2r1",
		Distro:          DistroRKE2,
		KubernetesImage: "docker.io/rancher/hardened-kubernetes:v1.36.4-rke2r1-build20260821",
		EtcdImage:       "docker.io/rancher/hardened-etcd:v3.6.14-k3s1-build20260819",
	},
	{
		Version:         "v1.36.4+k3s1",
		Distro:          DistroK3s,
		KubernetesImage: "docker.io/rancher/k3s:v1.36.4-k3s1",
	},
}

// Lookup returns the images for version.
func Lookup(version string) (Release, error) {
	for _, rel := range releases {
		if rel.Version == version {
			return rel, nil
		}
	}
	return Release{}, fmt.Errorf("kubernetes version %q is not in the catalog", version)
}

// CanUpgrade reports whether moving from one catalog version to another is allowed.
// The same version is allowed. A downgrade is not. A minor may increase by at most one.
func CanUpgrade(from, to string) error {
	if from == to {
		return nil
	}
	if family(from) != family(to) {
		return fmt.Errorf("cannot change distribution from %s to %s", from, to)
	}
	fromV, err := parse(from)
	if err != nil {
		return fmt.Errorf("current version: %w", err)
	}
	toV, err := parse(to)
	if err != nil {
		return fmt.Errorf("target version: %w", err)
	}
	if toV.major != fromV.major {
		return fmt.Errorf("cannot change major version from %s to %s", from, to)
	}
	switch {
	case toV.minor < fromV.minor:
		return fmt.Errorf("downgrade from %s to %s is not supported", from, to)
	case toV.minor == fromV.minor && toV.patch < fromV.patch:
		return fmt.Errorf("downgrade from %s to %s is not supported", from, to)
	case toV.minor > fromV.minor+1:
		return fmt.Errorf("cannot skip minor versions from %s to %s", from, to)
	default:
		return nil
	}
}

type version struct {
	major, minor, patch int
}

func family(raw string) string {
	switch {
	case strings.Contains(raw, "+k3s"):
		return DistroK3s
	case strings.Contains(raw, "+rke2"):
		return DistroRKE2
	default:
		return ""
	}
}

func parse(raw string) (version, error) {
	s := strings.TrimPrefix(raw, "v")
	s, _, _ = strings.Cut(s, "+")
	s, _, _ = strings.Cut(s, "-")
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return version{}, fmt.Errorf("version %q is not a Kubernetes version", raw)
	}
	nums := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return version{}, fmt.Errorf("version %q: %w", raw, err)
		}
		nums[i] = n
	}
	return version{major: nums[0], minor: nums[1], patch: nums[2]}, nil
}
