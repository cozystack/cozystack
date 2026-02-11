# Managed File Share Service

NFS Ganesha based managed file sharing service that provides NFSv4 network storage accessible from multiple clients simultaneously.

## Deployment Details

This service deploys NFS Ganesha as a StatefulSet with persistent volume storage, exposing NFSv4 on port 2049.

- Docs: <https://github.com/nfs-ganesha/nfs-ganesha/wiki>
- GitHub: <https://github.com/nfs-ganesha/nfs-ganesha>

## Parameters

### Common parameters

| Name           | Description                          | Type       | Value        |
| -------------- | ------------------------------------ | ---------- | ------------ |
| `storageClass` | StorageClass used to store the data. | `string`   | `replicated` |
| `size`         | Volume size for NFS storage.         | `quantity` | `10Gi`       |

