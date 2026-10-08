package controlplane

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rancher/rke2-silicon/internal/catalog"
	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
)

// Snapshot saves an etcd snapshot into the data directory and returns the host path.
// Failure here must stop an upgrade before any container is replaced.
func Snapshot(ctx context.Context, cfg Config) (string, error) {
	cfg = cfg.withDefaults()
	if err := prepare(cfg); err != nil {
		return "", err
	}
	if cfg.Distro == catalog.DistroK3s {
		if cfg.Datastore == config.DatastoreSQLite {
			return snapshotSQLite(cfg)
		}
		return snapshotK3sEtcd(ctx, cfg)
	}
	name := fmt.Sprintf("etcd-snapshot-%s.db", time.Now().UTC().Format("20060102-150405"))
	hostPath := filepath.Join(cfg.DataDir, "server", "db", "snapshots", name)
	guestPath := guest(layout.GuestRoot, "server", "db", "snapshots", name)
	if err := runEtcdctl(ctx, cfg, "snapshot", "save", guestPath); err != nil {
		return "", err
	}
	return hostPath, nil
}

// WaitEtcd blocks until etcd answers an endpoint health check.
func WaitEtcd(ctx context.Context, cfg Config) error {
	cfg = cfg.withDefaults()
	deadline := time.Now().Add(90 * time.Second)
	var last error
	for {
		last = runEtcdctl(ctx, cfg, "endpoint", "health")
		if last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("etcd did not become healthy: %w", last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func runEtcdctl(ctx context.Context, cfg Config, args ...string) error {
	tls := guest(layout.GuestRoot, "server", "tls", "etcd")
	spec := runtime.RunSpec{
		Name:       containerEtcdctl,
		Image:      cfg.EtcdImage,
		Network:    cfg.Network,
		Entrypoint: guest(layout.GuestRoot, "busybox"),
		CPUs:       "1",
		Memory:     "256M",
		Mounts: []runtime.Mount{{
			Source: cfg.DataDir,
			Target: layout.GuestRoot,
		}},
		Env: []string{
			"ETCDCTL_API=3",
			"ETCDCTL_ENDPOINTS=https://etcd:2379",
			"ETCDCTL_CACERT=" + tls + "/server-ca.crt",
			"ETCDCTL_CERT=" + tls + "/client.crt",
			"ETCDCTL_KEY=" + tls + "/client.key",
		},
		Args: append([]string{"sh", "-c", etcdctlScript(), "sh", "/usr/local/bin/etcdctl"}, args...),
	}
	_ = cfg.Runtime.Delete(ctx, containerEtcdctl)
	defer cfg.Runtime.Delete(context.Background(), containerEtcdctl)
	if err := cfg.Runtime.RunForeground(ctx, spec); err != nil {
		return fmt.Errorf("etcdctl %s: %w", args[0], err)
	}
	return nil
}

func snapshotSQLite(cfg Config) (string, error) {
	srcDir := filepath.Join(cfg.DataDir, "server", "db")
	db := filepath.Join(srcDir, "state.db")
	if _, err := os.Stat(db); err != nil {
		return "", fmt.Errorf("sqlite datastore %s: %w", db, err)
	}
	dest := filepath.Join(srcDir, "snapshots", "sqlite-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(dest, 0o777); err != nil {
		return "", err
	}
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		if err := copyIfExists(filepath.Join(srcDir, name), filepath.Join(dest, name)); err != nil {
			return "", err
		}
	}
	return dest, nil
}

func copyIfExists(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func snapshotK3sEtcd(ctx context.Context, cfg Config) (string, error) {
	if cfg.Runtime == nil {
		return "", fmt.Errorf("container runtime is required")
	}
	name := "upgrade-" + time.Now().UTC().Format("20060102-150405")
	err := cfg.Runtime.Exec(ctx, containerK3s,
		"etcd-snapshot", "save",
		"--data-dir="+layout.GuestRoot,
		"--name="+name,
	)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cfg.DataDir, "server", "db", "snapshots")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var match string
	var newest time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.Contains(entry.Name(), name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", err
		}
		if match == "" || info.ModTime().After(newest) {
			newest = info.ModTime()
			match = filepath.Join(dir, entry.Name())
		}
	}
	if match == "" {
		return "", fmt.Errorf("k3s etcd-snapshot wrote no file matching %s in %s", name, dir)
	}
	return match, nil
}

func etcdctlScript() string {
	return fmt.Sprintf(`set -eu
bb=%s/busybox
"$bb" cat %s/hosts > /etc/hosts
exec "$@"`, layout.GuestRoot, layout.GuestRoot)
}
