# VM Import Adoption Design

## Contexte

Forklift importe des VMs depuis VMware et crée des objets `VirtualMachine` KubeVirt natifs.
Cozystack gère les VMs via des applications Helm (`vm-instance`, `vm-disk`).

**Problème** : Les VMs importées ne sont pas visibles/gérables via le dashboard Cozystack car elles ne sont pas créées via les applications Helm standard.

## Solution proposée

### 1. Lifecycle des ressources

#### Ressources Helm (gérées par vm-import)
- `Provider` (source et destination)
- `NetworkMap`
- `StorageMap`
- `Plan`
- `Migration`

**Comportement à la suppression de vm-import** :
- Les `Providers` sont conservés avec `helm.sh/resource-policy: keep` (peuvent être réutilisés)
- Les autres ressources (Plan, Migration, Maps) sont supprimées (objets temporaires de migration)

#### Ressources créées par Forklift (NON gérées par Helm)
- `VirtualMachine` (KubeVirt)
- `DataVolume` (CDI)
- `PersistentVolumeClaim`

**Comportement à la suppression de vm-import** :
- **JAMAIS supprimées** car elles ne font pas partie de la release Helm
- Les VMs restent opérationnelles et gérables via `kubectl`

### 2. Mécanisme d'adoption

#### Phase 1 : Labels automatiques (via Forklift)

Les VMs créées par Forklift ont déjà des labels :
```yaml
metadata:
  labels:
    forklift.konveyor.io/plan: <plan-name>
    forklift.konveyor.io/vm-name: <vm-name>
```

On ajoute via annotations sur le Plan :
```yaml
metadata:
  annotations:
    vm-import.cozystack.io/adoption-enabled: "true"
    vm-import.cozystack.io/target-namespace: {{ .Release.Namespace }}
```

#### Phase 2 : Controller d'adoption (nouveau composant)

Un controller léger (`vm-import-adoption-controller`) :

1. **Surveille** les VMs avec label `forklift.konveyor.io/plan`
2. **Détecte** l'annotation `vm-import.cozystack.io/adoption-enabled` sur le Plan correspondant
3. **Adopte** la VM en ajoutant des labels Cozystack :
   ```yaml
   labels:
     cozystack.io/adopted: "true"
     cozystack.io/source: "vm-import"
     cozystack.io/original-plan: <plan-name>
     app.kubernetes.io/managed-by: "cozystack"
   ```

4. **Crée** des objets ConfigMap pour le tracking :
   ```yaml
   apiVersion: v1
   kind: ConfigMap
   metadata:
     name: vm-adopted-<vm-name>
     namespace: <namespace>
     labels:
       cozystack.io/type: "adopted-vm"
   data:
     vmName: <vm-name>
     sourcePlan: <plan-name>
     sourceType: "vmware"
     adoptedAt: <timestamp>
     managementEndpoint: "kubectl"
   ```

#### Phase 3 : Visibilité dans le dashboard

Le dashboard Cozystack affiche les VMs avec le label `cozystack.io/adopted: "true"` dans une section "Imported VMs".

Options de gestion :
- **View only** : Afficher l'état, les métriques
- **Basic operations** : Start/Stop via KubeVirt API
- **Advanced** : Créer une release `vm-instance` pour adoption complète (gestion Helm)

### 3. Adoption complète (optionnelle)

Un utilisateur peut "fully adopt" une VM en créant manuellement une release `vm-instance` qui référence la VM existante.

**Script helper fourni** : `adopt-vm.sh`
```bash
#!/bin/bash
# adopt-vm.sh <vm-name> <namespace>
# Crée une HelmRelease vm-instance qui adopte une VM existante

VM_NAME=$1
NAMESPACE=$2

# Extrait les specs de la VM existante
kubectl get vm "$VM_NAME" -n "$NAMESPACE" -o yaml > /tmp/vm-spec.yaml

# Génère values.yaml pour vm-instance
# (mapping des specs existantes vers le format vm-instance)

# Crée la HelmRelease avec adoption
cat <<EOF | kubectl apply -f -
apiVersion: helm.toolkit.fluxcd.io/v2beta1
kind: HelmRelease
metadata:
  name: $VM_NAME
  namespace: $NAMESPACE
spec:
  chart:
    spec:
      chart: vm-instance
      sourceRef:
        kind: HelmRepository
        name: cozystack
  install:
    createNamespace: false
    # N'essaie pas de créer la VM (elle existe déjà)
    disableWait: true
  upgrade:
    # Ne modifie pas la VM existante
    force: false
  values:
    # Valeurs extraites de la VM existante
    ...
EOF
```

### 4. Implémentation

#### Fichiers à modifier

1. **packages/apps/vm-import/templates/plan.yaml**
   ```yaml
   metadata:
     annotations:
       vm-import.cozystack.io/adoption-enabled: "true"
       vm-import.cozystack.io/target-namespace: {{ .Release.Namespace }}
   ```

2. **packages/apps/vm-import/templates/provider.yaml**
   ```yaml
   metadata:
     annotations:
       helm.sh/resource-policy: keep  # Providers conservés
   ```

3. **packages/apps/vm-import/values.yaml**
   ```yaml
   ## @param {bool} enableAdoption - Automatically label imported VMs for Cozystack adoption.
   enableAdoption: true
   ```

#### Nouveau package : vm-import-adoption-controller

Structure :
```
packages/system/vm-import-adoption-controller/
├── Chart.yaml
├── templates/
│   ├── deployment.yaml          # Controller deployment
│   ├── serviceaccount.yaml
│   ├── role.yaml                # RBAC: watch VMs, Plans; create ConfigMaps
│   └── rolebinding.yaml
└── images/
    └── Dockerfile               # Controller image
        └── main.go              # Controller logic
```

Controller logic (pseudo-code) :
```go
// Watch VirtualMachines with label "forklift.konveyor.io/plan"
// For each VM:
//   1. Get the Plan referenced
//   2. Check if Plan has annotation "vm-import.cozystack.io/adoption-enabled"
//   3. If yes, add Cozystack labels to VM
//   4. Create adoption ConfigMap
```

### 5. Documentation

#### README.md amélioré

```markdown
## VM Lifecycle

### Import Phase
When you create a `vm-import` application:
1. Forklift migrates VMs from VMware to KubeVirt
2. VMs are created as native KubeVirt `VirtualMachine` resources
3. If `enableAdoption: true` (default), VMs are automatically labeled for Cozystack tracking

### Post-Import
Imported VMs are:
- ✅ **Visible** in the Cozystack dashboard (as "Imported VMs")
- ✅ **Manageable** via `kubectl` and KubeVirt APIs
- ✅ **Persistent** - they are NOT deleted when you remove the `vm-import` application

### Adoption Options

#### Option 1: Dashboard Management (Automatic)
VMs are automatically labeled and appear in the dashboard. Basic operations (view, start, stop) available.

#### Option 2: Full Helm Management (Manual)
Create a `vm-instance` application referencing the existing VM:
```bash
./adopt-vm.sh my-imported-vm my-namespace
```

### Cleanup
To delete a `vm-import` application:
```bash
kubectl delete vmimport my-import -n my-namespace
```

**What gets deleted**:
- ❌ Migration Plan
- ❌ Network/Storage Maps
- ✅ Providers (kept for reuse)

**What is preserved**:
- ✅ All imported VMs
- ✅ All DataVolumes and disks
- ✅ Adoption tracking ConfigMaps
```

## Avantages de cette approche

1. **Non-destructif** : Suppression de vm-import ne supprime jamais les VMs
2. **Flexible** : Les utilisateurs choisissent le niveau d'adoption (basic vs full Helm)
3. **Traçable** : ConfigMaps gardent l'historique d'import
4. **Évolutif** : Le controller peut être enrichi progressivement
5. **Compatible** : Les VMs restent des objets KubeVirt standard

## Migration Path

### Court terme (MVP)
1. Ajouter annotations sur Plan/Provider
2. Documenter clairement le lifecycle
3. Fournir script `adopt-vm.sh`

### Moyen terme
1. Implémenter controller d'adoption
2. Intégrer dans le dashboard

### Long terme
1. Adoption Helm automatique (optionnelle)
2. Synchronisation bidirectionnelle VM <-> Helm
