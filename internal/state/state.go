// Package state stores the last cluster config that came up healthy.
package state

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/rancher/rke2-silicon/internal/config"
)

// Images are the container images that were actually started.
type Images struct {
	KubernetesVersion string `json:"kubernetesVersion,omitempty"`
	KubernetesImage   string `json:"kubernetesImage,omitempty"`
	EtcdImage         string `json:"etcdImage,omitempty"`
}

// JoinCredential is retained so an older cluster.rkestate file still loads.
type JoinCredential struct {
	CertPEM string `json:"certPEM"`
	KeyPEM  string `json:"keyPEM"`
}

// FullState is the cluster.rkestate file.
type FullState struct {
	Desired config.Cluster            `json:"desired"`
	Current *config.Cluster           `json:"current,omitempty"`
	Images  Images                    `json:"images,omitempty"`
	Join    map[string]JoinCredential `json:"join,omitempty"`
}

// Load reads the state file. A missing file is an empty state.
func Load(path string) (FullState, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FullState{}, nil
		}
		return FullState{}, err
	}
	var st FullState
	if err := json.Unmarshal(b, &st); err != nil {
		return FullState{}, err
	}
	return st, nil
}

// Save writes the state file for the owner only.
func (s FullState) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o600)
}
