// Package cluster implements up and upgrade for one Mac.
package cluster

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/node"
	"github.com/rancher/rke2-silicon/internal/state"
)

// Options are the flags shared by the lifecycle commands.
type Options struct {
	ConfigPath   string
	DataDir      string
	ContainerBin string
	// Purge removes containers and the host state up created.
	Purge bool
}

func (o Options) statePath() string {
	return filepath.Join(o.DataDir, layout.StateName)
}

func (o Options) loadConfig() (config.Cluster, error) {
	if o.ConfigPath == "" {
		return config.Cluster{}, fmt.Errorf("--config is required")
	}
	c, err := config.Load(o.ConfigPath)
	if err != nil {
		return config.Cluster{}, err
	}
	if c.NodeName == "" {
		c.NodeName, err = node.Name()
		if err != nil {
			return config.Cluster{}, err
		}
	}
	return c, nil
}

func (o Options) requireControlPlane(cluster config.Cluster) error {
	me, err := node.Name()
	if err != nil {
		return err
	}
	if node.Sanitize(me) != node.Sanitize(cluster.NodeName) {
		return fmt.Errorf("this machine is %s; node-name is %s", me, cluster.NodeName)
	}
	return nil
}

// RejectDatastoreChange refuses to replace sqlite with etcd, or the reverse.
// An empty datastore on a cluster recorded before the field existed is etcd
// for an RKE2 version and sqlite for a k3s version.
func RejectDatastoreChange(current *config.Cluster, desired config.Cluster) error {
	if current == nil || current.KubernetesVersion == "" {
		return nil
	}
	from := config.EffectiveDatastore(*current)
	if from != desired.Datastore {
		return fmt.Errorf("datastore is %s; changing it to %s is not supported", from, desired.Datastore)
	}
	return nil
}

// RejectVersionChange tells the operator to use upgrade when kubernetes-version moved.
func RejectVersionChange(current *config.Cluster, desired string) error {
	if current == nil || current.KubernetesVersion == "" || current.KubernetesVersion == desired {
		return nil
	}
	return fmt.Errorf("kubernetes-version changed from %s to %s; run rke2-silicon upgrade --config", current.KubernetesVersion, desired)
}

func saveState(path string, st state.FullState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return st.Save(path)
}

func imagesChanged(st state.FullState, version, k8sImage, etcdImage string) bool {
	if st.Current == nil || st.Current.KubernetesVersion != version {
		return true
	}
	return st.Images.KubernetesImage != k8sImage || st.Images.EtcdImage != etcdImage
}
