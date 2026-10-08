package ingress

import (
	"context"
	"fmt"

	"github.com/rancher/rke2-silicon/internal/layout"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	traefikName  = "traefik"
	traefikImage = "docker.io/library/traefik:v3.5.3"
	roleName     = "rke2-silicon-traefik"
	className    = "rke2-silicon"
)

// Ensure installs Traefik in kube-system. The pod is labeled so the agent
// publishes 80 and 443 on the reserved address and on no other pod.
func Ensure(ctx context.Context, cs kubernetes.Interface, ingressIP string) error {
	if err := ensureServiceAccount(ctx, cs); err != nil {
		return err
	}
	if err := ensureRBAC(ctx, cs); err != nil {
		return err
	}
	if err := ensureClass(ctx, cs); err != nil {
		return err
	}
	return ensureDeployment(ctx, cs, ingressIP)
}

func ensureServiceAccount(ctx context.Context, cs kubernetes.Interface) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: traefikName, Namespace: layout.SystemNamespace}}
	_, err := cs.CoreV1().ServiceAccounts(layout.SystemNamespace).Create(ctx, sa, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func ensureRBAC(ctx context.Context, cs kubernetes.Interface) error {
	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"services", "endpoints", "secrets", "nodes"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses", "ingressclasses"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses/status"}, Verbs: []string{"update"}},
		},
	}
	if _, err := cs.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return err
		}
		existing, getErr := cs.RbacV1().ClusterRoles().Get(ctx, roleName, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		existing.Rules = role.Rules
		if _, err := cs.RbacV1().ClusterRoles().Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: roleName},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      traefikName,
			Namespace: layout.SystemNamespace,
		}},
		RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: roleName},
	}
	_, err := cs.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func ensureClass(ctx context.Context, cs kubernetes.Interface) error {
	class := &networkingv1.IngressClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: className,
			Annotations: map[string]string{
				"ingressclass.kubernetes.io/is-default-class": "true",
			},
		},
		Spec: networkingv1.IngressClassSpec{Controller: "traefik.io/ingress-controller"},
	}
	_, err := cs.NetworkingV1().IngressClasses().Create(ctx, class, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

func ensureDeployment(ctx context.Context, cs kubernetes.Interface, ingressIP string) error {
	dep := traefikDeployment(ingressIP)
	existing, err := cs.AppsV1().Deployments(layout.SystemNamespace).Get(ctx, traefikName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.AppsV1().Deployments(layout.SystemNamespace).Create(ctx, dep, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Spec = dep.Spec
	_, err = cs.AppsV1().Deployments(layout.SystemNamespace).Update(ctx, existing, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update traefik: %w", err)
	}
	return nil
}

func traefikDeployment(ingressIP string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: traefikName, Namespace: layout.SystemNamespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": traefikName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
					"app":               traefikName,
					layout.IngressLabel: "true",
				}},
				Spec: corev1.PodSpec{
					ServiceAccountName: traefikName,
					Containers: []corev1.Container{{
						Name:  traefikName,
						Image: traefikImage,
						Args: []string{
							"--entrypoints.web.address=:80",
							"--entrypoints.websecure.address=:443",
							"--providers.kubernetesingress=true",
							"--providers.kubernetesingress.ingressclass=" + className,
							"--providers.kubernetesingress.ingressendpoint.ip=" + ingressIP,
							"--providers.kubernetescrd=false",
							"--api.dashboard=false",
						},
						Ports: []corev1.ContainerPort{
							{Name: "web", ContainerPort: 80},
							{Name: "websecure", ContainerPort: 443},
						},
					}},
				},
			},
		},
	}
}
