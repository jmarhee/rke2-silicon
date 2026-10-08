// Package pods reconciles single-container pods onto Apple Containers.
package pods

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/rancher/rke2-silicon/internal/gpu"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
	"github.com/rancher/rke2-silicon/internal/volumes"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

// ErrUnsupportedPod is returned for pods this agent will not run.
var ErrUnsupportedPod = errors.New("rke2-silicon only runs single-container pods without init containers")

// GPUAccess is the host broker a pod reaches when it reserves apple.com/gpu.
type GPUAccess struct {
	URL   string
	Token string
}

// Reconciler starts and stops Apple Containers for pods scheduled to one node.
type Reconciler struct {
	Client         kubernetes.Interface
	Runtime        *runtime.Client
	NodeName       string
	Network        string
	GPU            *GPUAccess
	DataDir        string
	IngressAddress string
}

// Run lists current pods and then watches for changes until ctx is canceled.
func (r *Reconciler) Run(ctx context.Context) error {
	for {
		if err := r.syncOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("pod sync failed", "err", err)
		}
		if err := r.watch(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("pod watch failed", "err", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (r *Reconciler) syncOnce(ctx context.Context) error {
	list, err := r.Client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", r.NodeName).String(),
	})
	if err != nil {
		return err
	}
	wanted := map[string]struct{}{}
	for i := range list.Items {
		pod := &list.Items[i]
		name := ContainerName(pod.Namespace, pod.Name)
		wanted[name] = struct{}{}
		if err := r.syncPod(ctx, pod); err != nil {
			slog.Error("sync pod", "namespace", pod.Namespace, "name", pod.Name, "err", err)
		}
	}
	existing, err := r.Runtime.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range existing {
		if !strings.HasPrefix(c.ID, layout.PodPrefix) {
			continue
		}
		if _, ok := wanted[c.ID]; ok {
			continue
		}
		slog.Info("removing container for a pod that is gone", "container", c.ID)
		_ = r.Runtime.Delete(ctx, c.ID)
	}
	return nil
}

func (r *Reconciler) watch(ctx context.Context) error {
	w, err := r.Client.CoreV1().Pods(metav1.NamespaceAll).Watch(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", r.NodeName).String(),
	})
	if err != nil {
		return err
	}
	defer w.Stop()
	resync := time.NewTicker(10 * time.Second)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resync.C:
			if err := r.syncOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Error("pod sync failed", "err", err)
			}
		case ev, ok := <-w.ResultChan():
			if !ok {
				return fmt.Errorf("pod watch closed")
			}
			pod, ok := ev.Object.(*corev1.Pod)
			if !ok || pod == nil {
				continue
			}
			if err := r.syncPod(ctx, pod); err != nil {
				slog.Error("sync pod", "namespace", pod.Namespace, "name", pod.Name, "err", err)
			}
		}
	}
}

func (r *Reconciler) syncPod(ctx context.Context, pod *corev1.Pod) error {
	name := ContainerName(pod.Namespace, pod.Name)
	if pod.DeletionTimestamp != nil {
		if err := r.Runtime.Delete(ctx, name); err != nil {
			return err
		}
		return r.finishDeletion(ctx, pod)
	}
	spec, err := SpecForPod(ctx, pod, r.Network, r.GPU, volumes.Context{Client: r.Client, DataDir: r.DataDir})
	if err != nil {
		reason := "UnsupportedPod"
		if errors.Is(err, volumes.ErrNotReady) {
			reason = "VolumeNotReady"
		}
		return r.markPending(ctx, pod, reason, err.Error())
	}
	spec.Publish = ingressPublish(pod, r.IngressAddress)
	current, err := r.Runtime.Inspect(ctx, name)
	if err != nil && !errors.Is(err, runtime.ErrNotFound) {
		return err
	}
	if errors.Is(err, runtime.ErrNotFound) {
		slog.Info("starting pod container", "namespace", pod.Namespace, "name", pod.Name, "image", spec.Image)
		if err := r.Runtime.Run(ctx, spec); err != nil {
			return err
		}
		current, err = r.Runtime.Inspect(ctx, name)
		if err != nil {
			return err
		}
	} else if current.State != "running" {
		if err := r.Runtime.Start(ctx, name); err != nil {
			return err
		}
		current, err = r.Runtime.Inspect(ctx, name)
		if err != nil {
			return err
		}
	}
	if current.State != "running" || current.IPv4 == "" {
		return r.markPending(ctx, pod, "ContainerNotRunning", fmt.Sprintf("container %s is %s", name, current.State))
	}
	if pod.Status.Phase == corev1.PodRunning && pod.Status.PodIP == current.IPv4 && containerReady(pod, spec.Name) {
		return nil
	}
	return r.markRunning(ctx, pod, current.IPv4, spec.Name)
}

func (r *Reconciler) finishDeletion(ctx context.Context, pod *corev1.Pod) error {
	latest, err := r.Client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(latest.Finalizers) > 0 {
		latest.Finalizers = nil
		latest, err = r.Client.CoreV1().Pods(pod.Namespace).Update(ctx, latest, metav1.UpdateOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	grace := int64(0)
	err = r.Client.CoreV1().Pods(pod.Namespace).Delete(ctx, latest.Name, metav1.DeleteOptions{GracePeriodSeconds: &grace})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func containerReady(pod *corev1.Pod, containerID string) bool {
	if len(pod.Status.ContainerStatuses) != 1 {
		return false
	}
	st := pod.Status.ContainerStatuses[0]
	return st.Ready && st.ContainerID == "apple://"+containerID && st.State.Running != nil
}

func (r *Reconciler) markRunning(ctx context.Context, pod *corev1.Pod, ip, containerName string) error {
	return retryStatus(ctx, func() error {
		latest, err := r.Client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		now := metav1.Now()
		if latest.Status.StartTime == nil {
			latest.Status.StartTime = &now
		}
		latest.Status.Phase = corev1.PodRunning
		latest.Status.PodIP = ip
		latest.Status.PodIPs = []corev1.PodIP{{IP: ip}}
		latest.Status.HostIP = layout.Gateway
		latest.Status.Reason = ""
		latest.Status.Message = ""
		container := latest.Spec.Containers[0]
		latest.Status.ContainerStatuses = []corev1.ContainerStatus{{
			Name:        container.Name,
			Image:       container.Image,
			ImageID:     container.Image,
			ContainerID: "apple://" + containerName,
			Ready:       true,
			Started:     ptr(true),
			State:       corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}},
		}}
		latest.Status.Conditions = upsertPodCondition(latest.Status.Conditions, corev1.PodInitialized, corev1.ConditionTrue, "RKE2Silicon", "pod has no init containers", now)
		latest.Status.Conditions = upsertPodCondition(latest.Status.Conditions, corev1.PodReady, corev1.ConditionTrue, "RKE2Silicon", "container is running", now)
		latest.Status.Conditions = upsertPodCondition(latest.Status.Conditions, corev1.ContainersReady, corev1.ConditionTrue, "RKE2Silicon", "container is running", now)
		_, err = r.Client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}

func (r *Reconciler) markPending(ctx context.Context, pod *corev1.Pod, reason, message string) error {
	if pod.Status.Message == message && pod.Status.Reason == reason && pod.Status.Phase == corev1.PodPending {
		return nil
	}
	return retryStatus(ctx, func() error {
		latest, err := r.Client.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		now := metav1.Now()
		latest.Status.Phase = corev1.PodPending
		latest.Status.Reason = reason
		latest.Status.Message = message
		latest.Status.Conditions = upsertPodCondition(latest.Status.Conditions, corev1.PodReady, corev1.ConditionFalse, reason, message, now)
		_, err = r.Client.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, latest, metav1.UpdateOptions{})
		return err
	})
}

func retryStatus(ctx context.Context, fn func() error) error {
	var err error
	for i := 0; i < 4; i++ {
		err = fn()
		if err == nil || !apierrors.IsConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return err
}

func upsertPodCondition(conds []corev1.PodCondition, typ corev1.PodConditionType, status corev1.ConditionStatus, reason, message string, now metav1.Time) []corev1.PodCondition {
	transition := now
	for i, c := range conds {
		if c.Type != typ {
			continue
		}
		if c.Status == status && !c.LastTransitionTime.IsZero() {
			transition = c.LastTransitionTime
		}
		conds[i] = corev1.PodCondition{
			Type:               typ,
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastProbeTime:      now,
			LastTransitionTime: transition,
		}
		return conds
	}
	return append(conds, corev1.PodCondition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastProbeTime:      now,
		LastTransitionTime: now,
	})
}

// ingressPublish binds the labeled pod to the unprivileged host ports on the
// reserved LAN address. A root forwarder accepts 80 and 443 there. Every
// other pod stays unpublished.
func ingressPublish(pod *corev1.Pod, addr string) []string {
	if addr == "" || pod == nil || pod.Labels[layout.IngressLabel] != "true" {
		return nil
	}
	return []string{
		addr + ":" + layout.IngressHTTP + ":80",
		addr + ":" + layout.IngressHTTPS + ":443",
	}
}

// ContainerName is the Apple container id for a pod.
func ContainerName(namespace, name string) string {
	base := layout.PodPrefix + namespace + "-" + name
	if len(base) <= 63 {
		return base
	}
	sum := sha256.Sum256([]byte(namespace + "/" + name))
	return layout.PodPrefix + hex.EncodeToString(sum[:16])
}

// SpecForPod translates one pod into a container run.
// gpu is required when the container reserves apple.com/gpu. The agent injects
// the broker URL, token, and share count. vol mounts hostPath, NFS, and
// projected service-account tokens. containerPort and hostPort are ignored.
func SpecForPod(ctx context.Context, pod *corev1.Pod, network string, gpu *GPUAccess, vol volumes.Context) (runtime.RunSpec, error) {
	if len(pod.Spec.InitContainers) > 0 || len(pod.Spec.Containers) != 1 {
		return runtime.RunSpec{}, ErrUnsupportedPod
	}
	c := pod.Spec.Containers[0]
	resolved, err := volumes.Resolve(ctx, pod, vol)
	if err != nil {
		return runtime.RunSpec{}, err
	}
	spec := runtime.RunSpec{
		Name:    ContainerName(pod.Namespace, pod.Name),
		Image:   c.Image,
		Network: network,
		CPUs:    "1",
		Memory:  "1G",
		WorkDir: c.WorkingDir,
		Mounts:  resolved.Mounts,
	}
	shares := gpuShares(c)
	if shares > 0 && (gpu == nil || gpu.URL == "" || gpu.Token == "") {
		return runtime.RunSpec{}, fmt.Errorf("pod requests %s but the gpu broker is not available", layout.GPUResource)
	}
	if len(c.Command) > 0 {
		spec.Entrypoint = c.Command[0]
		spec.Args = append(append([]string{}, c.Command[1:]...), c.Args...)
	} else {
		spec.Args = append([]string{}, c.Args...)
	}
	skipAPI := len(resolved.Env) > 0
	for _, env := range c.Env {
		if reservedGPUEnv(env.Name) {
			continue
		}
		if skipAPI && (env.Name == "KUBERNETES_SERVICE_HOST" || env.Name == "KUBERNETES_SERVICE_PORT") {
			continue
		}
		line, ok := containerEnv(pod, env)
		if !ok {
			continue
		}
		spec.Env = append(spec.Env, line)
	}
	if shares > 0 {
		spec.Env = append(spec.Env, gpuEnv(gpu, shares)...)
	}
	spec.Env = append(spec.Env, resolved.Env...)
	return spec, nil
}

func gpuShares(c corev1.Container) int64 {
	name := corev1.ResourceName(layout.GPUResource)
	if q, ok := c.Resources.Limits[name]; ok {
		return q.Value()
	}
	if q, ok := c.Resources.Requests[name]; ok {
		return q.Value()
	}
	return 0
}

// containerEnv returns a NAME=value line. Field refs use the downward API
// paths a pod can read about itself. Other valueFrom sources are left unset.
func containerEnv(pod *corev1.Pod, env corev1.EnvVar) (string, bool) {
	if env.ValueFrom == nil {
		return env.Name + "=" + env.Value, true
	}
	if env.ValueFrom.FieldRef == nil {
		return "", false
	}
	value, ok := fieldPath(pod, env.ValueFrom.FieldRef.FieldPath)
	if !ok {
		return "", false
	}
	return env.Name + "=" + value, true
}

func fieldPath(pod *corev1.Pod, path string) (string, bool) {
	switch path {
	case "metadata.name":
		return pod.Name, true
	case "metadata.namespace":
		return pod.Namespace, true
	case "metadata.uid":
		return string(pod.UID), true
	case "spec.nodeName":
		return pod.Spec.NodeName, true
	case "spec.serviceAccountName":
		return pod.Spec.ServiceAccountName, true
	case "status.podIP":
		return pod.Status.PodIP, true
	default:
		return "", false
	}
}

func reservedGPUEnv(name string) bool {
	switch name {
	case gpu.EnvURL, gpu.EnvToken, gpu.EnvShares:
		return true
	default:
		return false
	}
}

func gpuEnv(access *GPUAccess, shares int64) []string {
	return []string{
		gpu.EnvURL + "=" + access.URL,
		gpu.EnvToken + "=" + access.Token,
		gpu.EnvShares + "=" + strconv.FormatInt(shares, 10),
	}
}

func ptr[T any](v T) *T { return &v }
