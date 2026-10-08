# Edge compute on this Mac

rke2-silicon runs one Kubernetes node on a Mac that is already at the site. The Mac has the LAN interface, the GPU, and the directories the instruments write. A pod reads or pulls here, processes on this machine, and writes to a store that already has an address.

Each container is a Linux micro-VM. The control plane is `etcd`, `kube-apiserver`, `kube-controller-manager`, and `kube-scheduler`, started from Rancher's hardened images. A LaunchAgent heartbeats the node and starts each pod as its own container. The RKE2 installer is never executed.

## The work

The useful path is local. A result leaves as a write, or as a connection the pod opens.

| Work | How it finishes |
| --- | --- |
| Line inspection | The pod dials a camera gateway, holds one GPU share, and appends the result. |
| Condition monitoring | The pod dials the source and writes a window to a store, or to an NFS export the Mac has mounted. |
| Instrument drop | A bench writes a host directory. The pod reads it and writes the report beside it. |
| Shift-batch inference | Pods read a spool and write scores. Four may hold the GPU at once. |

Metal stays on the host. `apple.com/gpu` is how many of those kernels the broker will run at the same time. The node advertises four. A pod calls the broker at `192.168.128.1:10443` and receives the result. The sample kernel adds one to each integer.

Files the site already has are directories on the Mac, or an NFS export the Mac has mounted. The pod sees them through virtiofs. A PersistentVolume is that directory.

## The LAN

`ingress-address` is an address on the site subnet, added as a `/32` on the Mac's LAN interface. The kernel delivers packets for it to this machine and answers ARP with that interface's MAC. A host process accepts port 80 and 443 there and connects them to Traefik. Traefik dials the backend pod on the container network, `192.168.128.0/24`.

A client on the site opens the ingress hostname. ClusterIP, NodePort, and LoadBalancer remain records on the apiserver. With one node, nothing else is waiting on an overlay.

## Beside an edge appliance

An edge Kubernetes appliance is a Linux board installed so the cluster can run there. kubelet and containerd live on that board. A CNI and kube-proxy give each pod an address another machine can open. A speaker such as MetalLB or kube-vip answers ARP so a LoadBalancer service owns a LAN address. A GPU, when the board has one, is a device handed to the guest.

This node starts from the computer the site already has. Kubernetes schedules Linux processes onto it, one kernel each. Metal, the LAN interface, and the files stay on the Mac.

| | Edge appliance | This Mac |
| --- | --- | --- |
| The computer | A board installed to run the cluster | The machine already on the LAN, with the GPU and the files |
| Isolation | Containers share the appliance kernel | One kernel per container |
| GPU | A device in the guest | A Metal kernel on the host; a pod holds a share |
| Address on the LAN | A cluster speaker answers ARP for a Service | The host kernel answers ARP for an alias on the LAN interface |
| How work leaves | Another machine opens a ClusterIP or a NodePort | The pod writes to a store, or dials out; Traefik is the way in |
| What is installed | kubelet, containerd, a CNI, and the distribution's charts | The control-plane processes as containers, and a LaunchAgent that starts pods |

The datastore stays on the Mac: etcd for an RKE2 release, sqlite or one embedded etcd member for K3s. A later cycle can point that at an endpoint that outlives the machine. This cycle keeps the state next to the work.
