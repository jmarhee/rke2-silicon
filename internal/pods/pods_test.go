package pods

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rancher/rke2-silicon/internal/volumes"

	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSpecForPodSingleContainer(t *testing.T) {
	dir := t.TempDir()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nginx"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:       "nginx",
				Image:      "docker.io/library/nginx:1.27",
				WorkingDir: "/tmp",
				Env: []corev1.EnvVar{
					{Name: "COLOR", Value: "blue"},
					{Name: "SKIP", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
				},
				Command: []string{"nginx"},
				Args:    []string{"-g", "daemon off;"},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "html",
					MountPath: "/usr/share/nginx/html",
					ReadOnly:  true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "html",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: dir},
				},
			}},
		},
	}
	spec, err := SpecForPod(context.Background(), pod, "rke2-silicon", nil, volumes.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "rke2-silicon-pod-default-nginx" || spec.Image != "docker.io/library/nginx:1.27" || spec.Network != "rke2-silicon" {
		t.Fatalf("identity = %+v", spec)
	}
	if spec.Entrypoint != "nginx" || len(spec.Args) != 2 || spec.Args[0] != "-g" {
		t.Fatalf("command = %s %v", spec.Entrypoint, spec.Args)
	}
	if len(spec.Env) != 2 || spec.Env[0] != "COLOR=blue" || spec.Env[1] != "SKIP=nginx" {
		t.Fatalf("env = %v", spec.Env)
	}
	if len(spec.Mounts) != 1 || spec.Mounts[0] != (runtime.Mount{Source: dir, Target: "/usr/share/nginx/html", ReadOnly: true}) {
		t.Fatalf("mounts = %+v", spec.Mounts)
	}
}

func TestSpecForPodRejectsMultipleContainers(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "two"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "a", Image: "a"}, {Name: "b", Image: "b"}},
		},
	}
	_, err := SpecForPod(context.Background(), pod, "rke2-silicon", nil, volumes.Context{})
	if !errors.Is(err, ErrUnsupportedPod) {
		t.Fatalf("err = %v", err)
	}
}

func TestSpecForPodRejectsHostPathFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "file"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "app",
				Image: "app",
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "cfg",
					MountPath: "/cfg",
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "cfg",
				VolumeSource: corev1.VolumeSource{
					HostPath: &corev1.HostPathVolumeSource{Path: file},
				},
			}},
		},
	}
	_, err := SpecForPod(context.Background(), pod, "rke2-silicon", nil, volumes.Context{})
	if err == nil {
		t.Fatal("expected file hostPath to fail")
	}
}

func TestSpecForPodInjectsGPUBroker(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "sample"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:  "sample",
				Image: "docker.io/curlimages/curl:8.12.1",
				Env: []corev1.EnvVar{
					{Name: "RKE_MACOS_GPU_URL", Value: "http://wrong"},
					{Name: "COLOR", Value: "blue"},
				},
				Resources: corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceName(layout.GPUResource): resource.MustParse("2"),
					},
				},
			}},
		},
	}
	_, err := SpecForPod(context.Background(), pod, "rke2-silicon", nil, volumes.Context{})
	if err == nil {
		t.Fatal("expected a gpu pod without a broker to fail")
	}
	spec, err := SpecForPod(context.Background(), pod, "rke2-silicon", &GPUAccess{URL: "http://192.168.128.1:10443", Token: "tok"}, volumes.Context{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, env := range spec.Env {
		name, value, _ := strings.Cut(env, "=")
		got[name] = value
	}
	if got["COLOR"] != "blue" || got["RKE_MACOS_GPU_URL"] != "http://192.168.128.1:10443" || got["RKE_MACOS_GPU_TOKEN"] != "tok" || got["RKE_MACOS_GPU_SHARES"] != "2" {
		t.Fatalf("env = %v", spec.Env)
	}
}

func TestIngressPublishOnlyForTheLabeledPod(t *testing.T) {
	plain := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "meter"}}
	if got := ingressPublish(plain, "192.168.0.200"); got != nil {
		t.Fatalf("publish = %v", got)
	}
	ingress := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{layout.IngressLabel: "true"}}}
	got := ingressPublish(ingress, "192.168.0.200")
	if len(got) != 2 || got[0] != "192.168.0.200:8080:80" || got[1] != "192.168.0.200:8443:443" {
		t.Fatalf("publish = %v", got)
	}
}

func TestContainerNameHashesLongNames(t *testing.T) {
	long := "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz012345"
	got := ContainerName("default", long)
	if len(got) > 63 || got == "rke2-silicon-pod-default-"+long {
		t.Fatalf("name = %s len %d", got, len(got))
	}
}
