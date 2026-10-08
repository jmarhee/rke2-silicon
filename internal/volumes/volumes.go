// Package volumes turns pod volumes into virtiofs mounts.
//
// An NFS volume, or a claim bound to an NFS volume, is mounted on the Mac and
// then exported into the guest. A hostPath volume, or a claim bound to a
// hostPath PersistentVolume, is that directory. A projected service
// account token is written on the Mac and exported the same way. The guest
// reaches the apiserver at the vmnet gateway, which is the address on the
// serving certificate.
package volumes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ErrNotReady means the pod should stay Pending and be reconciled again.
var ErrNotReady = errors.New("volume not ready")

// APIHost is the address in-cluster clients use. It is the vmnet gateway.
const APIHost = layout.Gateway

// APIPort is the apiserver port on APIHost.
const APIPort = layout.APIPort

// Mounter attaches an NFS export to a host directory.
type Mounter interface {
	EnsureNFS(server, export, dest string) error
}

// Context is the host state Resolve needs.
type Context struct {
	Client  kubernetes.Interface
	DataDir string
	Mount   Mounter
}

// Result is the mounts and environment for one pod.
type Result struct {
	Mounts []runtime.Mount
	Env    []string
}

// Resolve collects virtiofs mounts for the pod's single container.
func Resolve(ctx context.Context, pod *corev1.Pod, c Context) (Result, error) {
	if pod == nil || len(pod.Spec.Containers) != 1 {
		return Result{}, nil
	}
	container := pod.Spec.Containers[0]
	var out Result
	token := false
	for _, vol := range pod.Spec.Volumes {
		mount, ok := volumeMount(container, vol.Name)
		if !ok {
			continue
		}
		switch {
		case vol.HostPath != nil:
			m, err := hostPathMount(vol.HostPath.Path, mount)
			if err != nil {
				return Result{}, err
			}
			out.Mounts = append(out.Mounts, m)
		case vol.NFS != nil:
			m, err := nfsMount(c, vol.NFS.Server, vol.NFS.Path, mount)
			if err != nil {
				return Result{}, err
			}
			out.Mounts = append(out.Mounts, m)
		case vol.PersistentVolumeClaim != nil:
			m, err := claimMount(ctx, c, pod.Namespace, vol.PersistentVolumeClaim.ClaimName, mount)
			if err != nil {
				return Result{}, err
			}
			out.Mounts = append(out.Mounts, m)
		case vol.Secret != nil:
			dir, err := writeSecret(ctx, c, pod, vol.Name, vol.Secret)
			if err != nil {
				return Result{}, err
			}
			out.Mounts = append(out.Mounts, runtime.Mount{
				Source:   dir,
				Target:   mount.MountPath,
				ReadOnly: true,
			})
		case vol.Projected != nil:
			dir, projectedToken, err := project(ctx, c, pod, vol.Name, vol.Projected)
			if err != nil {
				return Result{}, err
			}
			token = token || projectedToken
			out.Mounts = append(out.Mounts, runtime.Mount{
				Source:   dir,
				Target:   mount.MountPath,
				ReadOnly: true,
			})
		}
	}
	if token {
		out.Env = []string{
			"KUBERNETES_SERVICE_HOST=" + APIHost,
			"KUBERNETES_SERVICE_PORT=" + APIPort,
		}
	}
	return out, nil
}

func volumeMount(c corev1.Container, name string) (corev1.VolumeMount, bool) {
	for _, m := range c.VolumeMounts {
		if m.Name == name {
			return m, true
		}
	}
	return corev1.VolumeMount{}, false
}

func hostPathMount(path string, mount corev1.VolumeMount) (runtime.Mount, error) {
	info, err := os.Stat(path)
	if err != nil {
		return runtime.Mount{}, fmt.Errorf("hostPath %s: %w", path, err)
	}
	if !info.IsDir() {
		return runtime.Mount{}, fmt.Errorf("hostPath %s must be a directory", path)
	}
	return runtime.Mount{Source: path, Target: mount.MountPath, ReadOnly: mount.ReadOnly}, nil
}

func nfsMount(c Context, server, export string, mount corev1.VolumeMount) (runtime.Mount, error) {
	if server == "" || export == "" {
		return runtime.Mount{}, fmt.Errorf("nfs volume %s requires a server and a path", mount.Name)
	}
	if strings.Contains(server, ",") || strings.Contains(export, ",") {
		return runtime.Mount{}, fmt.Errorf("nfs volume %s paths cannot contain commas", mount.Name)
	}
	if c.DataDir == "" || c.mounter() == nil {
		return runtime.Mount{}, fmt.Errorf("nfs volume %s requires the node agent", mount.Name)
	}
	sum := sha256.Sum256([]byte(server + "\n" + export))
	dest := filepath.Join(c.DataDir, "volumes", hex.EncodeToString(sum[:8]))
	if err := c.mounter().EnsureNFS(server, export, dest); err != nil {
		return runtime.Mount{}, fmt.Errorf("nfs %s:%s: %w", server, export, err)
	}
	readOnly := mount.ReadOnly
	return runtime.Mount{Source: dest, Target: mount.MountPath, ReadOnly: readOnly}, nil
}

func claimMount(ctx context.Context, c Context, namespace, name string, mount corev1.VolumeMount) (runtime.Mount, error) {
	if c.Client == nil {
		return runtime.Mount{}, fmt.Errorf("persistentvolumeclaim %s requires the node agent", name)
	}
	pvc, err := c.Client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return runtime.Mount{}, err
	}
	if pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" {
		return runtime.Mount{}, fmt.Errorf("%w: persistentvolumeclaim %s/%s is not bound", ErrNotReady, namespace, name)
	}
	pv, err := c.Client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return runtime.Mount{}, err
	}
	switch {
	case pv.Spec.HostPath != nil:
		return hostPathMount(pv.Spec.HostPath.Path, mount)
	case pv.Spec.NFS != nil:
		if pv.Spec.NFS.ReadOnly {
			mount.ReadOnly = true
		}
		return nfsMount(c, pv.Spec.NFS.Server, pv.Spec.NFS.Path, mount)
	default:
		return runtime.Mount{}, fmt.Errorf("persistentvolume %s is not a hostPath directory or an NFS volume", pv.Name)
	}
}

func (c Context) mounter() Mounter {
	if c.Mount != nil {
		return c.Mount
	}
	if c.DataDir == "" {
		return nil
	}
	return HostMounter{}
}

func writeSecret(ctx context.Context, c Context, pod *corev1.Pod, volName string, src *corev1.SecretVolumeSource) (string, error) {
	if c.DataDir == "" || c.Client == nil {
		return "", fmt.Errorf("secret volume %s requires the node agent", volName)
	}
	secret, err := c.Client.CoreV1().Secrets(pod.Namespace).Get(ctx, src.SecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && src.Optional != nil && *src.Optional {
		dir := filepath.Join(c.DataDir, "pods", pod.Namespace, pod.Name, volName)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return "", err
		}
		return dir, nil
	}
	if err != nil {
		return "", err
	}
	dir := filepath.Join(c.DataDir, "pods", pod.Namespace, pod.Name, volName)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	mode := os.FileMode(0o644)
	if src.DefaultMode != nil {
		mode = os.FileMode(*src.DefaultMode)
	}
	files := map[string][]byte{}
	if len(src.Items) == 0 {
		for key, value := range secret.Data {
			files[key] = value
		}
	} else {
		for _, item := range src.Items {
			value, ok := secret.Data[item.Key]
			if !ok {
				return "", fmt.Errorf("secret %s has no key %s", src.SecretName, item.Key)
			}
			name := item.Path
			if name == "" {
				name = item.Key
			}
			files[name] = value
		}
	}
	for name, value := range files {
		dest, err := projectedPath(dir, name)
		if err != nil {
			return "", err
		}
		// defaultMode is often read-only. A later reconcile has to replace the file.
		if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err := os.WriteFile(dest, value, mode); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func project(ctx context.Context, c Context, pod *corev1.Pod, volName string, src *corev1.ProjectedVolumeSource) (string, bool, error) {
	if c.DataDir == "" || c.Client == nil {
		return "", false, fmt.Errorf("projected volume %s requires the node agent", volName)
	}
	dir := filepath.Join(c.DataDir, "pods", pod.Namespace, pod.Name, volName)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", false, err
	}
	token := false
	for _, source := range src.Sources {
		switch {
		case source.ServiceAccountToken != nil:
			if err := writeToken(ctx, c.Client, pod, dir, source.ServiceAccountToken); err != nil {
				return "", false, err
			}
			token = true
		case source.ConfigMap != nil:
			if err := writeConfigMap(ctx, c, pod.Namespace, dir, source.ConfigMap); err != nil {
				return "", false, err
			}
		case source.DownwardAPI != nil:
			if err := writeDownward(pod, dir, source.DownwardAPI); err != nil {
				return "", false, err
			}
		default:
			return "", false, fmt.Errorf("projected volume %s has an unsupported source", volName)
		}
	}
	return dir, token, nil
}

func writeToken(ctx context.Context, cs kubernetes.Interface, pod *corev1.Pod, dir string, src *corev1.ServiceAccountTokenProjection) error {
	sa := pod.Spec.ServiceAccountName
	if sa == "" {
		sa = "default"
	}
	aud := src.Audience
	if aud == "" {
		aud = "https://kubernetes.default.svc.cluster.local"
	}
	exp := int64(3600)
	if src.ExpirationSeconds != nil && *src.ExpirationSeconds > 0 {
		exp = *src.ExpirationSeconds
	}
	tr, err := cs.CoreV1().ServiceAccounts(pod.Namespace).CreateToken(ctx, sa, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences:         []string{aud},
			ExpirationSeconds: &exp,
			BoundObjectRef: &authenticationv1.BoundObjectReference{
				APIVersion: "v1",
				Kind:       "Pod",
				Name:       pod.Name,
				UID:        pod.UID,
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("service account token: %w", err)
	}
	dest, err := projectedPath(dir, src.Path)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, []byte(tr.Status.Token), 0o644)
}

func writeConfigMap(ctx context.Context, c Context, namespace, dir string, src *corev1.ConfigMapProjection) error {
	cm, err := c.Client.CoreV1().ConfigMaps(namespace).Get(ctx, src.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && src.Name == "kube-root-ca.crt" {
		b, readErr := os.ReadFile(filepath.Join(c.DataDir, "server", "tls", "server-ca.crt"))
		if readErr != nil {
			return readErr
		}
		for _, item := range src.Items {
			if item.Key != "ca.crt" {
				continue
			}
			dest, pathErr := projectedPath(dir, item.Path)
			if pathErr != nil {
				return pathErr
			}
			if err := os.WriteFile(dest, b, 0o644); err != nil {
				return err
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	for _, item := range src.Items {
		value, ok := cm.Data[item.Key]
		if !ok {
			return fmt.Errorf("configmap %s has no key %s", src.Name, item.Key)
		}
		dest, pathErr := projectedPath(dir, item.Path)
		if pathErr != nil {
			return pathErr
		}
		if err := os.WriteFile(dest, []byte(value), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeDownward(pod *corev1.Pod, dir string, src *corev1.DownwardAPIProjection) error {
	for _, item := range src.Items {
		if item.FieldRef == nil {
			return fmt.Errorf("downward API item %s is not a field ref", item.Path)
		}
		var value string
		switch item.FieldRef.FieldPath {
		case "metadata.namespace":
			value = pod.Namespace
		case "metadata.name":
			value = pod.Name
		default:
			return fmt.Errorf("downward API field %s is unsupported", item.FieldRef.FieldPath)
		}
		dest, err := projectedPath(dir, item.Path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(value), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func projectedPath(dir, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("projected path %q is invalid", name)
	}
	dest := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
		return "", err
	}
	return dest, nil
}
