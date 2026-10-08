package ingress

import (
	"context"
	"testing"

	"github.com/rancher/rke2-silicon/internal/layout"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureTraefik(t *testing.T) {
	cs := fake.NewSimpleClientset()
	if err := Ensure(context.Background(), cs, "192.168.0.200"); err != nil {
		t.Fatal(err)
	}
	dep, err := cs.AppsV1().Deployments(layout.SystemNamespace).Get(context.Background(), traefikName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if dep.Spec.Template.Labels[layout.IngressLabel] != "true" {
		t.Fatalf("labels = %v", dep.Spec.Template.Labels)
	}
	args := dep.Spec.Template.Spec.Containers[0].Args
	found := false
	for _, arg := range args {
		if arg == "--providers.kubernetesingress.ingressendpoint.ip=192.168.0.200" {
			found = true
		}
	}
	if !found {
		t.Fatalf("args = %v", args)
	}
	if err := Ensure(context.Background(), cs, "192.168.0.200"); err != nil {
		t.Fatal(err)
	}
}
