package volumes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type dirMounter struct{}

func (dirMounter) EnsureNFS(_, _, dest string) error {
	return os.MkdirAll(dest, 0o755)
}

func TestResolveNFSClaim(t *testing.T) {
	dir := t.TempDir()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "meter-log"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "pvc-1"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				NFS: &corev1.NFSVolumeSource{Server: "192.168.1.50", Path: "/export/default-meter-log"},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "meter-log"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "meter",
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "data",
					MountPath: "/data",
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "meter-log"},
				},
			}},
		},
	}
	got, err := Resolve(context.Background(), pod, Context{
		Client:  fake.NewSimpleClientset(pvc, pv),
		DataDir: dir,
		Mount:   dirMounter{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].Target != "/data" || got.Mounts[0].ReadOnly {
		t.Fatalf("mounts = %+v", got.Mounts)
	}
	info, err := os.Stat(got.Mounts[0].Source)
	if err != nil || !info.IsDir() {
		t.Fatalf("source %s: %v", got.Mounts[0].Source, err)
	}
}

func TestResolveHostPathClaim(t *testing.T) {
	dir := t.TempDir()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench"},
		Spec:       corev1.PersistentVolumeClaimSpec{VolumeName: "bench"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
	}
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "bench"},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				HostPath: &corev1.HostPathVolumeSource{Path: dir},
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "bench"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:         "parser",
				VolumeMounts: []corev1.VolumeMount{{Name: "report", MountPath: "/report"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "report",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "bench"},
				},
			}},
		},
	}
	got, err := Resolve(context.Background(), pod, Context{Client: fake.NewSimpleClientset(pvc, pv)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].Source != dir || got.Mounts[0].Target != "/report" {
		t.Fatalf("mounts = %+v", got.Mounts)
	}
}

func TestResolveSecretVolume(t *testing.T) {
	dir := t.TempDir()
	mode := int32(256)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cattle-system", Name: "cattle-credentials"},
		Data:       map[string][]byte{"url": []byte("https://rancher.example")},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "cattle-system", Name: "agent"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name: "agent",
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "cattle-credentials",
					MountPath: "/cattle-credentials",
					ReadOnly:  true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "cattle-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{SecretName: "cattle-credentials", DefaultMode: &mode},
				},
			}},
		},
	}
	got, err := Resolve(context.Background(), pod, Context{Client: fake.NewSimpleClientset(secret), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Mounts) != 1 || got.Mounts[0].Target != "/cattle-credentials" || !got.Mounts[0].ReadOnly {
		t.Fatalf("mounts = %+v", got.Mounts)
	}
	b, err := os.ReadFile(filepath.Join(got.Mounts[0].Source, "url"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "https://rancher.example" {
		t.Fatalf("url = %s", b)
	}
}

func TestResolveUnboundClaim(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "meter-log"},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "meter-log"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{
				Name:         "meter",
				VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data"}},
			}},
			Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "meter-log"},
				},
			}},
		},
	}
	_, err := Resolve(context.Background(), pod, Context{Client: fake.NewSimpleClientset(pvc), DataDir: t.TempDir(), Mount: dirMounter{}})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveProjectedToken(t *testing.T) {
	dir := t.TempDir()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nfs-client-provisioner", UID: "uid-1"},
		Spec: corev1.PodSpec{
			ServiceAccountName: "nfs-client-provisioner",
			Containers: []corev1.Container{{
				Name: "nfs-client-provisioner",
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "sa",
					MountPath: "/var/run/secrets/kubernetes.io/serviceaccount",
					ReadOnly:  true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "sa",
				VolumeSource: corev1.VolumeSource{
					Projected: &corev1.ProjectedVolumeSource{
						Sources: []corev1.VolumeProjection{
							{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token"}},
							{ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{Name: "kube-root-ca.crt"},
								Items:                []corev1.KeyToPath{{Key: "ca.crt", Path: "ca.crt"}},
							}},
							{DownwardAPI: &corev1.DownwardAPIProjection{Items: []corev1.DownwardAPIVolumeFile{{
								Path: "namespace",
								FieldRef: &corev1.ObjectFieldSelector{
									FieldPath: "metadata.namespace",
								},
							}}}},
						},
					},
				},
			}},
		},
	}
	if err := os.MkdirAll(filepath.Join(dir, "server", "tls"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server", "tls", "server-ca.crt"), []byte("CA"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewSimpleClientset()
	cs.PrependReactor("create", "serviceaccounts", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "token" {
			return false, nil, nil
		}
		return true, &authenticationv1.TokenRequest{
			Status: authenticationv1.TokenRequestStatus{Token: "signed-token"},
		}, nil
	})
	got, err := Resolve(context.Background(), pod, Context{Client: cs, DataDir: dir, Mount: dirMounter{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Env) != 2 || got.Env[0] != "KUBERNETES_SERVICE_HOST=192.168.128.1" || got.Env[1] != "KUBERNETES_SERVICE_PORT=6443" {
		t.Fatalf("env = %v", got.Env)
	}
	base := got.Mounts[0].Source
	token, err := os.ReadFile(filepath.Join(base, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if string(token) != "signed-token" {
		t.Fatalf("token = %s", token)
	}
	ns, err := os.ReadFile(filepath.Join(base, "namespace"))
	if err != nil || string(ns) != "default" {
		t.Fatalf("namespace = %s %v", ns, err)
	}
	ca, err := os.ReadFile(filepath.Join(base, "ca.crt"))
	if err != nil || string(ca) != "CA" {
		t.Fatalf("ca = %s %v", ca, err)
	}
}
