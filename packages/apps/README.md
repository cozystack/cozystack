### How to test packages local

```bash
cd packages/core/installer
make image-cozystack REGISTRY=YOUR_CUSTOM_REGISTRY
make apply
kubectl delete po -l app=source-controller -n cozy-fluxcd
```
