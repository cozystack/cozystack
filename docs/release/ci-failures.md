## CI failures release engineers commonly hit

| Symptom | Class | Diagnosis | Block release? |
|---------|-------|-----------|----------------|
| `Draft release for vX.Y.Z not found` on every release-* merge | Workflow regression — `Publish draft release` lost its `github-token` in a past refactor; default `GITHUB_TOKEN` cannot list drafts | At `pull-requests-release.yaml:163`. Undraft manually, file the fix | No |
| Changelog Copilot job returns 402 | Token quota — `COPILOT_GITHUB_TOKEN` premium-request quota empty | Both changelog entry points hit it: the optional rc-time `changelog-rc` dispatch and the promote-time `changelog` job (both call the reusable `changelog-generate.yaml`), plus the `tags.yaml` backstop | No — `continue-on-error: true` on the AI step, generate changelog by hand following [`agents/changelog.md`](../agents/changelog.md) |
| `Prepare Release` skipped and the run red, `Build arm64 images` failed | The rc's arm64 build failed | Read the `build-arm64` log; `build-arm64-nightly.yaml` usually failed the same way first. Fix on the line, cut the next rc | Yes — an rc is not published amd64 only |
| `Stitch multi-arch indexes and republish` fails listing refs | `verify-multiarch.sh` found refs that are not amd64+arm64 indexes, or that it could not check | Each listed ref names its reason. Single-platform manifest or index without one architecture: make the image multi-arch, or allowlist its repository with a reason if it cannot be. Manifest unreadable: correct the ref or digest; the allowlist does not excuse it. Rate-limited or unavailable after 3 tries: re-run once the throttle clears | Yes |
| E2E `kubernetes-test` fails with `CSINode does not contain driver csi.kubevirt.io` | Test ordering bug | Tenant's `kubernetes-${test_name}-csi` HelmRelease still installing when NFS PVC is created. Fix: wait for `csinode/<node>` to advertise the driver before creating the PVC | Yes — flaky and masks real CSI bugs |
| `tenant-root` HR in perpetual upgrade-rollback loop after OIDC patch | Real bug at the intersection of #2602's readiness change and the cozystack reconcile cadence | Look for `rate: Wait(n=1) would exceed context deadline` in operator logs; multiple tenant child HRs stuck `InProgress` | Yes |
| `cozy-dashboard` ImagePullBackOff from `cozystack-ui:latest` | Registry flake | Known noisy on some OCI mirrors; not currently in the prepull set | No — retry |
| PR CI: `docker push` 409 on `:latest` for the same image across two PRs | Concurrent manifest race on a shared floating tag | The `:latest` tag was previously written by every `make image` regardless of PR. Resolved by the image-tag refactor (`IMAGE_TAG=pr-<N>-<sha>` in PR builds, `IMAGE_TAG=<ref_name>` only at release time) | No — `gh run rerun --failed` |

**Heuristic:** workflow-token and Copilot-quota issues are out-of-band — release the tag, fix the workflow in a follow-up PR. Test ordering bugs and tenant-root reconcile storms are real and should block.

If you find yourself doing the same manual fixup on two consecutive releases (e.g. "undraft the release"), open a workflow-regression issue. Workflow bugs with known manual workarounds rot silently for months.
