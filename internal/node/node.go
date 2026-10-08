// Package node registers this Mac as a Kubernetes node and keeps it Ready.
package node

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/layout"
	coordv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	heartbeatEvery = 10 * time.Second
	leaseDuration  = int32(40)

	// controlPlaneRoleLabel is the label kubectl uses for the ROLES column.
	controlPlaneRoleLabel = "node-role.kubernetes.io/control-plane"
	workerRoleLabel       = "node-role.kubernetes.io/worker"
)

// EnsureObject creates the node and sets its role labels.
// It does not mark the node Ready.
func EnsureObject(ctx context.Context, cs kubernetes.Interface, name string, roles []string, info Info) error {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: nodeLabels(name, roles, info),
		},
	}
	if _, err := cs.CoreV1().Nodes().Create(ctx, node, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create node: %w", err)
	}
	return ensureIdentity(ctx, cs, name, roles, info)
}

// Ensure creates the node and posts the first Ready heartbeat.
func Ensure(ctx context.Context, cs kubernetes.Interface, name, internalIP string, roles []string, info Info) error {
	if err := EnsureObject(ctx, cs, name, roles, info); err != nil && !apierrors.IsForbidden(err) {
		return err
	}
	if err := ensureLeaseNamespace(ctx, cs); err != nil && !apierrors.IsForbidden(err) {
		return err
	}
	return HeartbeatOnce(ctx, cs, name, internalIP, roles, info)
}

// Heartbeat posts node status and renews the node lease until ctx is canceled.
// info is called on each tick so a version change shows up without a restart.
func Heartbeat(ctx context.Context, cs kubernetes.Interface, name, internalIP string, roles []string, info func() Info) {
	ticker := time.NewTicker(heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := HeartbeatOnce(ctx, cs, name, internalIP, roles, info()); err != nil && ctx.Err() == nil {
				slog.Error("node heartbeat failed", "err", err)
			}
		}
	}
}

// HeartbeatOnce updates Ready and the kube-node-lease object.
func HeartbeatOnce(ctx context.Context, cs kubernetes.Interface, name, internalIP string, roles []string, info Info) error {
	if err := ensureIdentity(ctx, cs, name, roles, info); err != nil {
		return err
	}
	if err := updateNodeStatus(ctx, cs, name, internalIP, info); err != nil {
		return err
	}
	if err := clearNotReadyTaints(ctx, cs, name); err != nil {
		return err
	}
	return renewLease(ctx, cs, name)
}

// clearNotReadyTaints drops the taints the node lifecycle controller adds
// before it has observed a Ready heartbeat. There is no control-plane taint:
// a one-node cluster stays schedulable.
func clearNotReadyTaints(ctx context.Context, cs kubernetes.Interface, name string) error {
	node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	kept := make([]corev1.Taint, 0, len(node.Spec.Taints))
	changed := false
	for _, taint := range node.Spec.Taints {
		if taint.Key == corev1.TaintNodeNotReady || taint.Key == corev1.TaintNodeUnreachable {
			changed = true
			continue
		}
		kept = append(kept, taint)
	}
	if !changed {
		return nil
	}
	node.Spec.Taints = kept
	_, err = cs.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("clear node taints: %w", err)
	}
	return nil
}

// nodeLabels keeps kubernetes.io/os=linux so Linux containers still schedule.
// The Mac itself is reported through status.nodeInfo.
func nodeLabels(name string, roles []string, info Info) map[string]string {
	arch := info.Architecture
	if arch == "" {
		arch = "arm64"
	}
	labels := map[string]string{
		corev1.LabelHostname:   name,
		corev1.LabelOSStable:   "linux",
		corev1.LabelArchStable: arch,
	}
	for _, role := range roles {
		switch role {
		case config.RoleControlPlane:
			labels[controlPlaneRoleLabel] = ""
		case config.RoleWorker:
			labels[workerRoleLabel] = ""
		}
	}
	return labels
}

func ensureIdentity(ctx context.Context, cs kubernetes.Interface, name string, roles []string, info Info) error {
	node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	changed := false
	for key, value := range nodeLabels(name, roles, info) {
		if current, ok := node.Labels[key]; !ok || current != value {
			node.Labels[key] = value
			changed = true
		}
	}
	if !changed {
		return nil
	}
	_, err = cs.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
	if apierrors.IsForbidden(err) {
		// NodeRestriction rejects role labels from a node client. The control
		// plane sets those with the admin kubeconfig.
		return nil
	}
	if err != nil {
		return fmt.Errorf("update node labels: %w", err)
	}
	return nil
}

func updateNodeStatus(ctx context.Context, cs kubernetes.Interface, name, internalIP string, info Info) error {
	node, err := cs.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	now := metav1.Now()
	node.Status.NodeInfo = corev1.NodeSystemInfo{
		OperatingSystem:         info.OperatingSystem,
		Architecture:            info.Architecture,
		OSImage:                 info.OSImage,
		KernelVersion:           info.KernelVersion,
		KubeletVersion:          info.KubeletVersion,
		ContainerRuntimeVersion: info.ContainerRuntimeVersion,
	}
	node.Status.Capacity = capacity(info.GPUShares)
	node.Status.Allocatable = capacity(info.GPUShares)
	node.Status.Addresses = []corev1.NodeAddress{
		{Type: corev1.NodeInternalIP, Address: internalIP},
		{Type: corev1.NodeHostName, Address: name},
	}
	node.Status.DaemonEndpoints = corev1.NodeDaemonEndpoints{
		KubeletEndpoint: corev1.DaemonEndpoint{Port: 10250},
	}
	node.Status.Conditions = []corev1.NodeCondition{
		readyCondition(node.Status.Conditions, corev1.NodeReady, corev1.ConditionTrue, "RKE2SiliconReady", "rke2-silicon is posting ready status", now),
		readyCondition(node.Status.Conditions, corev1.NodeMemoryPressure, corev1.ConditionFalse, "RKE2SiliconOK", "rke2-silicon does not report pressure", now),
		readyCondition(node.Status.Conditions, corev1.NodeDiskPressure, corev1.ConditionFalse, "RKE2SiliconOK", "rke2-silicon does not report pressure", now),
		readyCondition(node.Status.Conditions, corev1.NodePIDPressure, corev1.ConditionFalse, "RKE2SiliconOK", "rke2-silicon does not report pressure", now),
		readyCondition(node.Status.Conditions, corev1.NodeNetworkUnavailable, corev1.ConditionFalse, "RKE2SiliconNetwork", "pods use the Apple container network", now),
	}
	_, err = cs.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update node status: %w", err)
	}
	return nil
}

func capacity(gpuShares int64) corev1.ResourceList {
	if gpuShares < 0 {
		gpuShares = 0
	}
	return corev1.ResourceList{
		corev1.ResourceCPU:                      resource.MustParse("4"),
		corev1.ResourceMemory:                   resource.MustParse("8Gi"),
		corev1.ResourcePods:                     resource.MustParse("110"),
		corev1.ResourceName(layout.GPUResource): *resource.NewQuantity(gpuShares, resource.DecimalSI),
	}
}

func readyCondition(prev []corev1.NodeCondition, typ corev1.NodeConditionType, status corev1.ConditionStatus, reason, message string, now metav1.Time) corev1.NodeCondition {
	transition := now
	for _, c := range prev {
		if c.Type == typ && c.Status == status && !c.LastTransitionTime.IsZero() {
			transition = c.LastTransitionTime
			break
		}
	}
	return corev1.NodeCondition{
		Type:               typ,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastHeartbeatTime:  now,
		LastTransitionTime: transition,
	}
}

func ensureLeaseNamespace(ctx context.Context, cs kubernetes.Interface) error {
	_, err := cs.CoreV1().Namespaces().Get(ctx, corev1.NamespaceNodeLease, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: corev1.NamespaceNodeLease},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s: %w", corev1.NamespaceNodeLease, err)
	}
	return nil
}

func renewLease(ctx context.Context, cs kubernetes.Interface, name string) error {
	now := metav1.NewMicroTime(time.Now())
	holder := name
	duration := leaseDuration
	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: corev1.NamespaceNodeLease},
		Spec: coordv1.LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
			RenewTime:            &now,
		},
	}
	existing, err := cs.CoordinationV1().Leases(corev1.NamespaceNodeLease).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		lease.Spec.AcquireTime = &now
		_, err = cs.CoordinationV1().Leases(corev1.NamespaceNodeLease).Create(ctx, lease, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create node lease: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	existing.Spec.HolderIdentity = &holder
	existing.Spec.LeaseDurationSeconds = &duration
	existing.Spec.RenewTime = &now
	_, err = cs.CoordinationV1().Leases(corev1.NamespaceNodeLease).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("renew node lease: %w", err)
	}
	return nil
}
