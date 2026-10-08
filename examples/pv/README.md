# Bench directory

A static PersistentVolume for the instrument-drop case. The volume is a directory on the Mac. The pod writes `drop.txt` there through virtiofs. A claim binds only when its PersistentVolume is that kind of directory, or an NFS export.

Create the directory before applying. It has to be writable by the guest, so mode `0777`.

```sh
sudo mkdir -p /Users/Shared/rke2-silicon/bench
sudo chmod 0777 /Users/Shared/rke2-silicon/bench
export KUBECONFIG=$HOME/.rke2-silicon/rke2-silicon.yaml
kubectl apply -f examples/pv/workload.yaml
kubectl rollout status deployment/bench
cat /Users/Shared/rke2-silicon/bench/drop.txt
```

`Bound` on `pvc/bench` means the directory is the volume. Replace `hostPath.path` to use another directory. The path is a directory. A file is rejected.

```sh
kubectl delete -f examples/pv/workload.yaml
```

`Retain` leaves the directory in place after the claim is deleted.
