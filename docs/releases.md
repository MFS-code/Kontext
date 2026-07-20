---
title: Releases
description: Version tags, GHCR images, digest-pinned install.yaml, upgrades, and uninstall.
sidebarTitle: Releases
---

# Release and image versioning

The current public release is `v0.1.0-alpha.1`. Kontext releases use
SemVer-compatible git tags:

```text
vMAJOR.MINOR.PATCH
vMAJOR.MINOR.PATCH-PRERELEASE
```

Build metadata suffixes (`+...`) are not supported because they are not valid
OCI tag characters.

## Published images

Each release publishes the same tag for four public GHCR images:

```text
ghcr.io/mfs-code/kontext-operator:<release-tag>
ghcr.io/mfs-code/kontext-echo:<release-tag>
ghcr.io/mfs-code/kontext-reporter:<release-tag>
ghcr.io/mfs-code/kontext-reference:<release-tag>
```

Every image index includes `linux/amd64` and `linux/arm64`. Releases do not
publish mutable `latest` or `dev` tags. The workflow refuses to overwrite an
existing release image tag, verifies anonymous pulls, and attaches an
`image-digests.json` asset containing immutable `image@sha256:...` references.

The workflow uses its repository token to make new GHCR packages public. If
the account's package policy denies visibility changes to that token, configure
a classic `PACKAGES_TOKEN` repository secret with `write:packages`; the
workflow uses it only for the visibility operation. Publication fails before a
GitHub release is created if anonymous access cannot be verified.

## Release checklist

- [ ] Bump the documented release version in `README.md`, `SECURITY.md`,
  `docs/quickstart.md`, `docs/releases.md`, `docs/service-workload.md`,
  `website/index.html`, `deploy/examples/v1alpha1/README.md`, and
  `.github/ISSUE_TEMPLATE/bug_report.yml`.

## Install

Install a tagged release on an existing cluster without cloning the repository
or building images:

```bash
VERSION=v0.1.0-alpha.1
kubectl apply -f \
  "https://github.com/MFS-code/Kontext/releases/download/${VERSION}/install.yaml"
```

`install.yaml` contains the CRDs, Namespace, RBAC, and controller Deployment.
Its operator and trusted reporter references use the immutable digests recorded
in `image-digests.json`, while `app.kubernetes.io/version` and
`kontext.dev/release` retain the human-readable release tag.

Given a downloaded digest manifest, the release artifact is reproducible from
the tagged source:

```bash
make release-manifest IMAGE_DIGESTS=image-digests.json
```

## Upgrade between alpha releases

`v1alpha1` does not imply compatibility between Kontext distribution versions.
Read the target release notes first, especially any CRD schema, defaulting,
runtime-contract, or status changes. Back up custom resources before upgrading:

```bash
kubectl get agents,agentruns --all-namespaces -o yaml \
  > kontext-custom-resources-backup.yaml
```

Apply the new release manifest over the existing installation and wait for the
controller rollout:

```bash
NEW_VERSION="<target-release-tag>"
kubectl apply -f \
  "https://github.com/MFS-code/Kontext/releases/download/${NEW_VERSION}/install.yaml"
kubectl rollout status deployment/controller-manager \
  --namespace kontext-system \
  --timeout=180s
```

Kubernetes updates the CRDs, RBAC, and Deployment declaratively; existing
`Agent` and `AgentRun` resources remain. Downgrades are not supported. Release
CI installs the previous published manifest when available, runs a workload,
applies the candidate manifest in place, and runs the registry-backed suite
before publishing the new GitHub release.

## Uninstall

To remove only the Kontext control plane while retaining the CRDs and custom
resources in other namespaces:

```bash
kubectl delete clusterrolebinding manager-rolebinding \
  --ignore-not-found=true
kubectl delete clusterrole manager-role \
  --ignore-not-found=true
kubectl delete namespace kontext-system \
  --ignore-not-found=true --wait=true
```

Deleting `kontext-system` removes the controller and its ServiceAccount.
`Agent` and `AgentRun` resources in other namespaces remain stored and can be
reconciled again by reapplying an install manifest. Any custom resources
created inside `kontext-system` are deleted with that Namespace.

To remove Kontext completely, including both CRDs:

```bash
VERSION=v0.1.0-alpha.1
kubectl delete -f \
  "https://github.com/MFS-code/Kontext/releases/download/${VERSION}/install.yaml" \
  --ignore-not-found=true --wait=true
```

Deleting a CRD deletes **all** custom resources of that kind across the
cluster. Back them up first if their history or results must be retained.
Release CI verifies both the retention procedure and complete removal.

## API relationship

The git tag, GitHub release, and image tags identify one version of the Kontext
distribution. They do not change the Kubernetes API group/version: current
releases serve `kontext.dev/v1alpha1`, and maintained runtimes emit the
versioned event and result contracts documented in the [API specification](/SPEC).

`v1alpha1` is intentionally unstable. A new Kontext alpha release may make
breaking CRD, runtime-contract, or status-shape changes while retaining the
same Kubernetes API version. Consumers must read the release notes and follow
the documented upgrade procedure rather than treating matching `v1alpha1`
labels as compatibility guarantees.
