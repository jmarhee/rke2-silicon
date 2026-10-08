// Package nodespec is the per-machine file the LaunchAgent reads.
package nodespec

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/rancher/rke2-silicon/internal/layout"
)

// Spec is node.json.
type Spec struct {
	Name              string   `json:"name"`
	Roles             []string `json:"roles"`
	InternalIP        string   `json:"internalIP"`
	IngressAddress    string   `json:"ingressAddress,omitempty"`
	Kubeconfig        string   `json:"kubeconfig"`
	Network           string   `json:"network"`
	KubernetesVersion string   `json:"kubernetesVersion"`
	ContainerBin      string   `json:"containerBin,omitempty"`
}

// Path is node.json inside the data directory.
func Path(dataDir string) string {
	return filepath.Join(dataDir, layout.NodeSpecName)
}

// Load reads node.json.
func Load(dataDir string) (Spec, error) {
	b, err := os.ReadFile(Path(dataDir))
	if err != nil {
		return Spec{}, err
	}
	var spec Spec
	if err := json.Unmarshal(b, &spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// Save writes node.json for the owner only.
func Save(dataDir string, spec Spec) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(Path(dataDir), b, 0o600)
}
