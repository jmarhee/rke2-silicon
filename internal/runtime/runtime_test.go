package runtime

import (
	"slices"
	"strings"
	"testing"
)

func TestRunArgvPlacesImageBeforeProcessArgs(t *testing.T) {
	argv, err := RunSpec{
		Name:       "rke2-silicon-etcd",
		Image:      "docker.io/rancher/hardened-etcd:tag",
		Network:    "rke2-silicon",
		Entrypoint: "/bin/sh",
		CPUs:       "1",
		Memory:     "1G",
		Mounts:     []Mount{{Source: "/Users/me/.rke2-silicon", Target: "/var/lib/rke2-silicon"}},
		Publish:    []string{"127.0.0.1:6443:6443"},
		Env:        []string{"FOO=bar"},
		Args:       []string{"-c", "exec etcd", "sh", "--data-dir=/data"},
	}.Argv()
	if err != nil {
		t.Fatal(err)
	}

	imageAt := slices.Index(argv, "docker.io/rancher/hardened-etcd:tag")
	if imageAt < 0 {
		t.Fatalf("image missing from %v", argv)
	}
	processAt := slices.Index(argv, "-c")
	if processAt < imageAt {
		t.Fatalf("process args must follow the image, argv=%v", argv)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"--detach",
		"--progress none",
		"--name rke2-silicon-etcd",
		"--network rke2-silicon",
		"--cpus 1",
		"--memory 1G",
		"--entrypoint /bin/sh",
		"--env FOO=bar",
		"--mount type=virtiofs,source=/Users/me/.rke2-silicon,target=/var/lib/rke2-silicon",
		"--publish 127.0.0.1:6443:6443",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv missing %q in %s", want, joined)
		}
	}
}

func TestForegroundArgvOmitsDetach(t *testing.T) {
	argv, err := RunSpec{Name: "snap", Image: "etcd"}.ForegroundArgv()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(argv, "--detach") {
		t.Fatalf("foreground argv = %v", argv)
	}
}

func TestRunArgvRejectsCommasInMounts(t *testing.T) {
	_, err := RunSpec{
		Name:   "x",
		Image:  "img",
		Mounts: []Mount{{Source: "/tmp/a,b", Target: "/mnt"}},
	}.Argv()
	if err == nil {
		t.Fatal("expected comma in mount path to fail")
	}
}

func TestParseContainersIPv4(t *testing.T) {
	raw := `[{"configuration":{"id":"rke2-silicon-etcd","networks":[{"network":"rke2-silicon"}]},"status":{"state":"running","networks":[{"ipv4Address":"192.168.128.2/24","network":"rke2-silicon"}]}}]`
	list, err := parseContainers(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].IPv4 != "192.168.128.2" || list[0].State != "running" || list[0].ID != "rke2-silicon-etcd" {
		t.Fatalf("parsed %+v", list)
	}
	if len(list[0].Networks) != 1 || list[0].Networks[0] != "rke2-silicon" {
		t.Fatalf("networks = %v", list[0].Networks)
	}
}

func TestSystemRunningAndNetworkListed(t *testing.T) {
	status := "FIELD VALUE\nstatus running\nclient.version 1.4.1\n"
	if !systemRunning(status) {
		t.Fatal("expected running status")
	}
	if systemRunning("status stopped\n") {
		t.Fatal("stopped is not running")
	}
	nets := "NETWORK SUBNET\ndefault 192.168.64.0/24\nrke2-silicon 192.168.128.0/24\n"
	if !networkListed(nets, "rke2-silicon") {
		t.Fatal("expected rke2-silicon network")
	}
	if networkListed(nets, "rke2-silicon-etcd") {
		t.Fatal("container-like name should not match a network prefix")
	}
}
