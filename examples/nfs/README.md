# NFS meter log

A collector appends readings to a file on an NFS export. Hosts that already mount the export read the file. This is the condition-monitoring case: the pod pulls a reading, writes it, and finishes the cycle.

`nfs-subdir-external-provisioner` watches claims that use the `nfs` StorageClass. For each bound claim it creates a directory on the export and a PersistentVolume for that directory. The agent mounts the export on the Mac with `/sbin/mount_nfs` and exports the mount into the pod with virtiofs. The NFS server must allow the Mac. NFS traffic is that mount.

The provisioner talks to the apiserver at `https://192.168.128.1:6443`. The agent writes the service-account token and sets `KUBERNETES_SERVICE_HOST`. A cluster created with this version lists `192.168.128.1` on the apiserver certificate. An existing cluster keeps the certificate from its first `up`. The provisioner requires that address on the certificate.

`/sbin/mount_nfs` runs as the LaunchAgent user, and the `resvport` option needs a privileged port. That user must be permitted to mount NFS. The export should accept NFSv3.

`192.0.2.10` and `/export/apparatus` in `provisioner.yaml` are placeholders. Replace both with the server and export this Mac can mount.

The cluster is already up, and `./rke2-silicon` is the binary the LaunchAgent is running. That binary mounts the export and writes the provisioner's service-account token.

```sh
export KUBECONFIG=$HOME/.rke2-silicon/rke2-silicon.yaml
kubectl apply -f examples/nfs/provisioner.yaml
kubectl rollout status deployment/nfs-client-provisioner
kubectl apply -f examples/nfs/workload.yaml
kubectl get pvc meter-log
```

`Bound` means the directory exists on the export. The collector writes `readings.csv` inside it. The process log stays empty. The file is the record.

```sh
kubectl delete -f examples/nfs/workload.yaml
kubectl delete -f examples/nfs/provisioner.yaml
```

Deleting the claim removes the provisioned directory. `archiveOnDelete` is `false`.
