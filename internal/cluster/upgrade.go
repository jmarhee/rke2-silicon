package cluster

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rancher/rke2-silicon/internal/agent"
	"github.com/rancher/rke2-silicon/internal/catalog"
	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/controlplane"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
	"github.com/rancher/rke2-silicon/internal/state"
)

// Upgrade rolls this Mac's control plane onto the version in config.yaml.
func Upgrade(ctx context.Context, opt Options) error {
	return upgradeControlPlane(ctx, opt)
}

func upgradeControlPlane(ctx context.Context, opt Options) error {
	cluster, err := opt.loadConfig()
	if err != nil {
		return err
	}
	rel, err := catalog.Lookup(cluster.KubernetesVersion)
	if err != nil {
		return err
	}
	cluster, err = cluster.ApplyRelease(rel.Distro)
	if err != nil {
		return err
	}
	if err := opt.requireControlPlane(cluster); err != nil {
		return err
	}
	st, err := state.Load(opt.statePath())
	if err != nil {
		return err
	}
	if st.Current == nil || st.Current.KubernetesVersion == "" {
		return fmt.Errorf("cluster has not been brought up; run rke2-silicon up --config %s", opt.ConfigPath)
	}
	if err := catalog.CanUpgrade(st.Current.KubernetesVersion, cluster.KubernetesVersion); err != nil {
		return err
	}
	if err := RejectDatastoreChange(st.Current, cluster); err != nil {
		return err
	}
	if !imagesChanged(st, rel.Version, rel.KubernetesImage, rel.EtcdImage) {
		slog.Info("already at this version", "version", rel.Version)
		cs, err := adminClient(opt.DataDir)
		if err != nil {
			return err
		}
		if err := EnsureVersion(ctx, cs, rel.Version); err != nil {
			return err
		}
		return agent.Install(ctx, opt.DataDir)
	}

	currentRel, err := catalog.Lookup(st.Current.KubernetesVersion)
	if err != nil {
		return err
	}
	currentEtcd := st.Images.EtcdImage
	if currentEtcd == "" {
		currentEtcd = currentRel.EtcdImage
	}
	rt := &runtime.Client{Bin: opt.ContainerBin}
	snapCfg := controlplane.Config{
		DataDir:         opt.DataDir,
		Distro:          currentRel.Distro,
		Datastore:       config.EffectiveDatastore(*st.Current),
		KubernetesImage: currentRel.KubernetesImage,
		EtcdImage:       currentEtcd,
		Network:         layout.NetworkName,
		APIAddress:      cluster.NodeIP,
		Runtime:         rt,
	}
	snap, err := controlplane.Snapshot(ctx, snapCfg)
	if err != nil {
		return fmt.Errorf("datastore snapshot failed, upgrade stopped: %w", err)
	}
	slog.Info("etcd snapshot saved", "path", snap)

	if err := bringUp(ctx, opt, cluster, rel, st); err != nil {
		return err
	}
	slog.Info("control plane upgraded", "version", rel.Version)
	return nil
}
