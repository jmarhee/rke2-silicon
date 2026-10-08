package cluster

import (
	"context"
	"fmt"

	"github.com/rancher/rke2-silicon/internal/layout"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const agentRoleName = "rke2-silicon-agent"

// EnsureVersion publishes the cluster version for the local agent.
func EnsureVersion(ctx context.Context, cs kubernetes.Interface, version string) error {
	if _, err := cs.CoreV1().Namespaces().Get(ctx, layout.SystemNamespace, metav1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: layout.SystemNamespace},
		}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create %s: %w", layout.SystemNamespace, err)
		}
	}
	if err := upsertConfigMap(ctx, cs, version); err != nil {
		return err
	}
	if err := upsertRole(ctx, cs); err != nil {
		return err
	}
	return upsertRoleBinding(ctx, cs)
}

func upsertConfigMap(ctx context.Context, cs kubernetes.Interface, version string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: layout.VersionConfigMap, Namespace: layout.SystemNamespace},
		Data:       map[string]string{layout.VersionKey: version},
	}
	existing, err := cs.CoreV1().ConfigMaps(layout.SystemNamespace).Get(ctx, layout.VersionConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().ConfigMaps(layout.SystemNamespace).Create(ctx, cm, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if existing.Data == nil {
		existing.Data = map[string]string{}
	}
	existing.Data[layout.VersionKey] = version
	_, err = cs.CoreV1().ConfigMaps(layout.SystemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func upsertRole(ctx context.Context, cs kubernetes.Interface) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: agentRoleName, Namespace: layout.SystemNamespace},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{""},
			Resources:     []string{"configmaps"},
			ResourceNames: []string{layout.VersionConfigMap},
			Verbs:         []string{"get"},
		}},
	}
	existing, err := cs.RbacV1().Roles(layout.SystemNamespace).Get(ctx, agentRoleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().Roles(layout.SystemNamespace).Create(ctx, role, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Rules = role.Rules
	_, err = cs.RbacV1().Roles(layout.SystemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

func upsertRoleBinding(ctx context.Context, cs kubernetes.Interface) error {
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: agentRoleName, Namespace: layout.SystemNamespace},
		Subjects: []rbacv1.Subject{{
			Kind:     "Group",
			Name:     "system:nodes",
			APIGroup: "rbac.authorization.k8s.io",
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     agentRoleName,
		},
	}
	_, err := cs.RbacV1().RoleBindings(layout.SystemNamespace).Get(ctx, agentRoleName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.RbacV1().RoleBindings(layout.SystemNamespace).Create(ctx, binding, metav1.CreateOptions{})
		return err
	}
	return err
}
