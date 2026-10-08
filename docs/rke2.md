# rke2-silicon and RKE2

rke2-silicon starts one Kubernetes node on a Mac. For an RKE2 release it runs that release's control-plane processes as Apple containers, and a LaunchAgent starts pods. The software is the supervisor for that node. The `rke2` binary is not in this tree and is never executed.

## What it takes from an RKE2 release

An RKE2 release is a version string such as `v1.36.4+rke2r1`, plus the hardened images published for it. `kubernetes-version` in `config.yaml` selects the row in the catalog. That row names `docker.io/rancher/hardened-kubernetes` and `docker.io/rancher/hardened-etcd`. Those are the images an RKE2 server uses for the same components.

`up` writes certificates on the Mac, mounts `~/.rke2-silicon` at `/var/lib/rke2-silicon`, and starts four containers:

| Process | Image | Binary |
| --- | --- | --- |
| etcd | hardened-etcd | `/usr/local/bin/etcd` |
| kube-apiserver | hardened-kubernetes | `/usr/local/bin/kube-apiserver` |
| kube-controller-manager | hardened-kubernetes | `/usr/local/bin/kube-controller-manager` |
| kube-scheduler | hardened-kubernetes | `/usr/local/bin/kube-scheduler` |

`config.yaml` uses RKE2's keys. Known keys become arguments on those processes. The processes do not read the file. `kube-apiserver-arg`, `etcd-arg`, `kube-controller-manager-arg`, and `kube-scheduler-arg` are appended, and the last copy of a flag wins.

The catalog also lists a K3s release, `v1.36.4+k3s1`, as one `k3s server` container with the agent and the packaged charts disabled. The same LaunchAgent starts pods. A node stays on the distribution it was created with.

## How a release moves through the node

The catalog is the list of releases this node can run. A release is available when its version and image tags are in that list. `up` and `upgrade` both resolve `kubernetes-version` through it. A version that is absent fails.

`up` brings up the selected release. A later `up` with the same version leaves the running containers in place. A changed `kubernetes-version` is rejected there. The move to another release is `upgrade`.

`upgrade` allows the same version, refuses a change of distribution (`+rke2` stays `+rke2`), refuses a change of major, refuses a downgrade, and refuses a jump of more than one Kubernetes minor. The datastore stays etcd. Before the new images start, `upgrade` writes an etcd snapshot under `~/.rke2-silicon/server/db/snapshots/`. It then starts the four containers from the images in the new catalog row. The running version is stored in `cluster.rkestate` and in the ConfigMap `kube-system/rke2-silicon-version`.

Adding a newer RKE2 release to this node means adding its version and image tags to the catalog, then running `upgrade` with that `kubernetes-version`.

## Deployment, beside a traditional RKE2 installation

A traditional installation runs the RKE2 install script or package. The `rke2` server process is the supervisor. It starts containerd and kubelet, runs the four control-plane processes, and applies the charts shipped in the release: the CNI, CoreDNS, the ingress controller, and the metrics server. Configuration is `/etc/rancher/rke2/config.yaml`, which that process reads. State lives under `/var/lib/rancher/rke2`. An upgrade installs a new `rke2` binary and restarts the service.

This deployment uses the release's component images and its version rules, and supplies its own supervisor.

| | Traditional RKE2 | This node |
| --- | --- | --- |
| Supervisor | The `rke2` server process | `rke2-silicon up`, then a LaunchAgent |
| Where processes run | containerd on the host | One Apple container per process, each a Linux micro-VM |
| What starts a pod | kubelet | The LaunchAgent |
| Configuration | `rke2` reads `config.yaml` | The same keys become process arguments |
| Certificates and data | Created under `/var/lib/rancher/rke2` | Created on the Mac in `~/.rke2-silicon`, mounted at `/var/lib/rke2-silicon` |
| Charts from the release | Applied by the server | Left unapplied. Traefik is a Deployment this installer applies |
| Pod network | The release's CNI, kube-proxy, and CoreDNS | The container network `192.168.128.0/24`. Pods reach each other there |
| Upgrade | A new `rke2` binary restarts the service | A new catalog row, an etcd snapshot, then the four containers roll |
