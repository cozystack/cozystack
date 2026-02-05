# VM Import - Test Report

**Date**: 2026-02-05
**Testeur**: Claude Code
**Version**: vmware-import branch
**Cluster**: Lab cluster (3 nodes, KubeVirt installed)

## Résumé

✅ **Tous les tests des templates Helm ont réussi**
⚠️ **Tests end-to-end nécessitent installation de Forklift** (non installé sur cluster lab)

## Tests effectués

### 1. ✅ Rendu des templates Helm

#### Test 1.1: Configuration de base
**Fichier**: `test-values.yaml`
```yaml
sourceUrl: "https://vcenter.example.com/sdk"
sourceSecretName: "test-secret"
vms:
  - id: "vm-42"
    name: "test-vm"
  - id: "vm-43"
    name: "test-vm-2"
warm: false
enableAdoption: true
networkMap:
  - sourceId: "network-1"
    destinationType: "pod"
storageMap:
  - sourceId: "datastore-1"
    storageClass: "replicated"
```

**Résultat**: ✅ PASS
- Plan créé avec annotations d'adoption :
  ```yaml
  annotations:
    vm-import.cozystack.io/adoption-enabled: "true"
    vm-import.cozystack.io/target-namespace: "tenant-test"
    vm-import.cozystack.io/import-name: "test-import"
  ```
- Champ `warm: false` correctement valorisé
- NetworkMap et StorageMap présents dans le Plan

**Providers**: ✅ PASS
- Annotation `helm.sh/resource-policy: keep` présente sur les deux Providers
- Source Provider configuré avec vSphere
- Destination Provider configuré avec OpenShift

**NetworkMap**: ✅ PASS
- Type `pod` correctement rendu
- Références aux Providers correctes

**StorageMap**: ✅ PASS
- StorageClass mappé correctement

**Migration**: ✅ PASS
- Référence au Plan correcte

---

#### Test 1.2: Warm migration + Multus + enableAdoption=false
**Fichier**: `test-values-warm.yaml`
```yaml
sourceUrl: "https://vcenter.example.com/sdk"
sourceSecretName: "test-secret"
vms:
  - id: "vm-100"
    name: "large-vm"
warm: true
enableAdoption: false
networkMap:
  - sourceId: "network-10"
    destinationType: "multus"
    destinationName: "production-net"
    destinationNamespace: "tenant-test"
storageMap:
  - sourceId: "datastore-ssd"
    storageClass: "fast-storage"
```

**Résultat**: ✅ PASS
- `warm: true` correctement rendu
- `adoption-enabled: "false"` correctement valorisé (bug initial corrigé)
- NetworkMap Multus avec `destinationName` et `destinationNamespace` corrects

**Bug détecté et corrigé**:
- ❌ Initialement: `enableAdoption: false` rendait `"true"` (problème avec `default true`)
- ✅ Corrigé: Utilisation de `hasKey` pour gérer correctement les booleans

---

#### Test 1.3: Validation des erreurs
**Fichier**: `test-values-invalid.yaml`
```yaml
networkMap:
  - sourceId: "network-10"
    destinationType: "invalid-type"  # Type invalide
```

**Résultat**: ✅ PASS
- Erreur correctement levée :
  ```
  Error: networkMap.destinationType must be 'pod' or 'multus', got "invalid-type"
  ```
- Validation fonctionne comme prévu (correction de CodeRabbit)

---

### 2. ⚠️ Tests end-to-end (non effectués)

**Raison**: Forklift n'est pas installé sur le cluster lab.

**Statut Forklift**:
- ❌ Namespace `konveyor-forklift` absent
- ❌ CRDs Forklift principaux absents (Provider, Plan, Migration)
- ✅ Quelques CRDs CDI Forklift présents (OpenstackVolumePopulator, OvirtVolumePopulator)

**Prérequis pour tests complets**:
1. Déployer package `forklift-operator`
2. Déployer package `forklift`
3. Avoir accès à un vCenter de test
4. Créer des VMs de test dans vCenter

**Tests qui nécessitent Forklift**:
- [ ] Test 2.1: Import réel de VMs depuis vCenter
- [ ] Test 2.2: Vérification des labels Forklift sur VMs créées
- [ ] Test 2.3: Suppression de vm-import et vérification persistence
- [ ] Test 2.4: Adoption Helm complète avec `adopt-vm.sh`

---

## 3. ✅ Validation du design

### Annotations d'adoption
```yaml
# Sur le Plan
metadata:
  annotations:
    vm-import.cozystack.io/adoption-enabled: "true"
    vm-import.cozystack.io/target-namespace: "tenant-test"
    vm-import.cozystack.io/import-name: "test-import"
```

**Verdict**: ✅ Implémentation correcte
- Les annotations seront disponibles pour un futur controller d'adoption
- Elles permettent de lier les VMs au Plan d'import

### Resource Policy Keep
```yaml
# Sur les Providers
metadata:
  annotations:
    helm.sh/resource-policy: keep
```

**Verdict**: ✅ Implémentation correcte
- Les Providers seront conservés lors de la suppression de l'app
- Permet la réutilisation pour de futurs imports

### Gestion du champ `warm`
```yaml
spec:
  warm: {{ default false .Values.warm }}
```

**Verdict**: ✅ Implémentation correcte
- Correction du bug CodeRabbit appliquée
- `false` par défaut, pas de valeur vide

---

## 4. ✅ Documentation

### README.md
- ✅ Section "VM Lifecycle and Adoption" complète
- ✅ Explication claire du comportement à la suppression
- ✅ 3 options de gestion documentées
- ✅ Instructions de cleanup
- ✅ Paramètre `enableAdoption` documenté

### Nouveaux documents
- ✅ `ADOPTION_DESIGN.md` - Design complet et architecture
- ✅ `docs/adoption.md` - Guide détaillé d'adoption Helm
- ✅ `docs/scripts/adopt-vm.sh` - Script helper (exécutable)
- ✅ `docs/examples/simple-import.yaml` - Exemple simple
- ✅ `docs/examples/advanced-import.yaml` - Exemple avancé
- ✅ `docs/examples/monitoring-import.md` - Guide monitoring

---

## 5. Corrections appliquées

### Bugs CodeRabbit
1. ✅ `warm: {{ .Values.warm }}` → `warm: {{ default false .Values.warm }}`
2. ✅ Validation `destinationType` avec `fail` pour types invalides
3. ✅ Condition `map:` avec `or` (déjà correcte)

### Bugs détectés pendant les tests
1. ✅ `enableAdoption: false` mal géré → Corrigé avec `hasKey`

---

## Recommandations

### Pour merge
✅ **Ready to merge** - Les templates fonctionnent correctement et la documentation est complète.

### Pour tests complets
1. **Installer Forklift** sur le cluster lab :
   ```bash
   # Déployer forklift-operator
   helm install forklift-operator packages/system/forklift-operator -n cozy-forklift --create-namespace

   # Déployer forklift controller
   helm install forklift packages/system/forklift -n cozy-forklift
   ```

2. **Configurer un environnement VMware de test** :
   - vCenter accessible
   - VMs de test (différentes tailles)
   - Credentials valides

3. **Tests end-to-end recommandés** :
   - Import simple (1 VM, cold)
   - Import avancé (multiple VMs, warm, Multus)
   - Suppression et vérification persistence
   - Adoption Helm complète

### Pour production
1. **Monitoring** : Ajouter des métriques Prometheus sur les imports
2. **Dashboard** : Intégrer l'affichage des VMs adoptées
3. **Controller** : Implémenter le controller d'adoption automatique
4. **Tests unitaires** : Ajouter tests helm-unittest

---

## Conclusion

**Status global**: ✅ **PASS** (tests templates)

Les modifications apportées sont **fonctionnelles et bien conçues** :
- ✅ Templates Helm corrects
- ✅ Validation des entrées
- ✅ Annotations pour adoption future
- ✅ Resource policy pour persistence
- ✅ Documentation complète
- ✅ Bugs corrigés

**Ready for**: Code review et merge
**Next steps**: Tests end-to-end avec Forklift installé

---

## Annexes

### Commandes de test utilisées

```bash
# Test rendu Plan
helm template test-import . --namespace tenant-test \
  --values test-values.yaml \
  --show-only templates/plan.yaml

# Test rendu Providers
helm template test-import . --namespace tenant-test \
  --values test-values.yaml \
  --show-only templates/provider.yaml

# Test warm + multus
helm template test-warm . --namespace tenant-test \
  --values test-values-warm.yaml \
  --show-only templates/plan.yaml

# Test validation
helm template test-invalid . --namespace tenant-test \
  --values test-values-invalid.yaml \
  --show-only templates/networkmap.yaml
```

### Environnement de test

```
Cluster: Lab (3 nodes)
Kubernetes: v1.34.3
kubectl: v1.35.0
helm: (version non vérifiée)
Namespaces Cozystack: ✅ Présents
KubeVirt: ✅ Installé (cozy-kubevirt)
Forklift: ❌ Non installé
```
