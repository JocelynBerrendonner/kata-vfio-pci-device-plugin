# kata-vfio-pci-device-plugin

A small, opinionated Kubernetes device plugin that advertises VFIO-bound
PCI devices as allocatable resources and hands them to containers as
[CDI](https://github.com/cncf-tags/container-device-interface) devices.

It is **Kata-aware** in two senses:

* The resources it exposes (`vfio.io/gpu`, `vfio.io/nvswitch`,
  `vfio.io/ib`, ...) are only
  useful when the consuming pod runs under a VM-isolating runtime such
  as Kata Containers. The plugin DaemonSet is therefore scheduled only
  on nodes labelled `kata.io/runtime=installed` (or whatever label you
  pick), so it never lights up on plain runc nodes by accident.
* It deliberately avoids the `cdi.k8s.io/*` *pod-level annotation*
  shortcut. containerd ≥2.1 only honours `Config.CDIDevices` (populated
  by device plugins) and *container*-level annotations — pod-level
  CDI annotations are silently ignored. Using a device plugin is the
  upstream-blessed path and the only one that actually flows VFIO
  devices into a Kata UVM today.

## Scope

For NVIDIA GPUs and NVSwitches in production, prefer the
[NVIDIA GPU Operator](https://github.com/NVIDIA/gpu-operator) +
[`k8s-device-plugin`](https://github.com/NVIDIA/k8s-device-plugin)
stack — those generate their own CDI specs and integrate with Kata
out of the box.

For SR-IOV NICs / IB VFs intended for cluster networking, the
[SR-IOV Network Device Plugin](https://github.com/k8snetworkplumbingwg/sriov-network-device-plugin)
+ Multus is the mainstream path.

This plugin fills the **third case**: arbitrary PCI devices already
bound to `vfio-pci` on the host, described by a CDI spec at
`/etc/cdi/*.yaml`. Useful for:

* Dev / test benches doing direct VM assignment experiments.
* Custom accelerators or research hardware with no first-party
  device plugin.
* IB VFs handed to a Kata UVM as raw VFIO (not through SR-IOV CNI).

## How it works

1. The DaemonSet pod mounts the host's `/etc/cdi/` directory.
2. On startup (and on filesystem change), the plugin reads every
   CDI spec file and groups devices by CDI kind (e.g. `vfio/gpu`,
   `vfio/nvswitch`, `vfio/ib`). Each kind becomes one Kubernetes
   extended resource (mirroring NVIDIA's `k8s-device-plugin`
   per-class pool model):
   * `vfio/gpu`      &rarr; `vfio.io/gpu`
   * `vfio/nvswitch` &rarr; `vfio.io/nvswitch`
   * `vfio/ib`       &rarr; `vfio.io/ib`
   * generic: `vendor/class` &rarr; `vendor.io/class`
3. Each individual device in the spec becomes one allocatable
   instance under that resource.
4. When kubelet calls `Allocate`, the plugin returns the matching
   CDI device IDs in `ContainerAllocateResponse.CDIDevices`.
   containerd injects them into the OCI spec; the Kata shim
   then cold-plugs the underlying VFIO devices into the UVM.

## Building

```sh
make build         # local binary at ./bin/kata-vfio-pci-device-plugin
make image         # OCI image, tag controlled by IMG / TAG
```

## Deploying

```sh
# Label the nodes that have devices bound to vfio-pci:
kubectl label node <node> kata.io/runtime=installed

# Deploy:
kubectl apply -f deploy/daemonset.yaml

# Verify resources show up on the node:
kubectl describe node <node> | grep vfio.io
```

## Consuming from a pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: gpu-test
spec:
  runtimeClassName: kata-openvmm   # or kata-clh
  containers:
  - name: workload
    image: archlinux:latest
    resources:
      limits:
        vfio.io/gpu:      8  # 8 NVIDIA GPUs from kind=vfio/gpu
        vfio.io/nvswitch: 6  # 6 NVSwitch bridges from kind=vfio/nvswitch
        vfio.io/ib:       2  # 2 IB HCAs from kind=vfio/ib
```

The plugin picks the requested number of free devices from each pool
(`vfio/gpu`, `vfio/nvswitch`, `vfio/ib`), returns their CDI names to
kubelet, and the Kata shim cold-plugs them into the UVM.

## License

MIT — see [`LICENSE`](LICENSE).
