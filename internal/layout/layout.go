// Package layout holds paths and network constants shared by the manager.
package layout

import (
	"os"
	"path/filepath"
)

const (
	// GuestRoot is the data directory inside every control-plane container.
	GuestRoot = "/var/lib/rke2-silicon"

	NetworkName = "rke2-silicon"
	Subnet      = "192.168.128.0/24"
	// Gateway is the vmnet address of the host on NetworkName.
	Gateway = "192.168.128.1"

	APIPort = "6443"

	// GPUResource is the extended resource a pod requests for one software share
	// of the host GPU. GPUPort is where the host broker listens, on the vmnet
	// gateway and on 127.0.0.1. GPUShares is how many in-flight kernels the
	// broker allows when a GPU is present.
	GPUResource = "apple.com/gpu"
	GPUPort     = "10443"
	GPUShares   = 4

	KubeconfigName   = "rke2-silicon.yaml"
	StateName        = "cluster.rkestate"
	NodeSpecName     = "node.json"
	VersionConfigMap = "rke2-silicon-version"
	VersionKey       = "kubernetesVersion"
	SystemNamespace  = "kube-system"
	AgentLabel       = "com.rke2-silicon.agent"
	ContainerPrefix  = "rke2-silicon-"
	PodPrefix        = "rke2-silicon-pod-"

	// IngressLabel marks the pod published on the reserved LAN address.
	// The container CLI cannot bind host ports below 1024, so the pod is
	// published on IngressHTTP and IngressHTTPS. A root forwarder accepts
	// 80 and 443 on that address and connects to those ports.
	IngressLabel = "rke2-silicon.io/ingress"
	IngressHTTP  = "8080"
	IngressHTTPS = "8443"
	ForwardLabel = "com.rke2-silicon.forward"
)

// DefaultDataDir is ~/.rke2-silicon.
func DefaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".rke2-silicon"), nil
}

// AgentPlist is the per-user LaunchAgent path.
func AgentPlist() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", AgentLabel+".plist"), nil
}
