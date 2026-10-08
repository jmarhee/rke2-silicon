# GPU sample

This pod reserves one software share of the host GPU and asks the broker on the Mac to run a Metal kernel. The container never opens a GPU device. The agent injects `RKE_MACOS_GPU_URL`, `RKE_MACOS_GPU_TOKEN`, and `RKE_MACOS_GPU_SHARES` because the pod limits `apple.com/gpu`.

The kernel adds one to each integer. A successful log ends with `"values":[2,3,4,5]`.

The cluster is already up, and `./rke2-silicon` is the binary the LaunchAgent is running:

```sh
export KUBECONFIG=$HOME/.rke2-silicon/rke2-silicon.yaml
kubectl apply -f examples/gpu/pod.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded pod/gpu-sample --timeout=180s
kubectl logs pod/gpu-sample
```

The first compute compiles the Metal helper with `xcrun swiftc`, so the wait includes that compile. Four such pods can run at once. A fifth stays Pending until a share is free. Two computes that together ask for more shares than are free receive HTTP 429 from the broker.

Remove it with `kubectl delete pod gpu-sample`.
