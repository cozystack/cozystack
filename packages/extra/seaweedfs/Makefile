NAME=seaweedfs

include ../../../hack/package.mk

.PHONY: test
test:
	helm unittest .
	$(MAKE) test-guard-fail-closed

# Sibling of the same target in packages/system/seaweedfs: this chart's copy of
# the naming guard must also refuse an UPGRADE it cannot see the cluster for.
# helm-unittest 1.0.3 ignores release.isUpgrade, so tests/guard_fail_closed_test.yaml
# can only cover the render-through side and passes with or without the canary —
# the refusal needs a real renderer.
.PHONY: test-guard-fail-closed
test-guard-fail-closed:
	@out=$$(helm template seaweedfs . -n tenant-root --is-upgrade \
	  --set topology=Simple --set replicationFactor=2 \
	  --set _namespace.host=example.org --set _namespace.ingress=tenant-root \
	  --set _cluster.issuer-name=letsencrypt-prod --set _cluster.solver=http01 2>&1); \
	echo "$$out" | grep -q 'refusing to upgrade blind' \
	  || { echo "FAIL: a blind upgrade rendered instead of refusing (naming-guard canary is not firing)"; echo "$$out" | head -5; exit 1; }
	@echo "ok: naming guard refuses a blind upgrade"

generate:
	cozyvalues-gen -v values.yaml -s values.schema.json -r README.md
	../../../hack/update-crd.sh
