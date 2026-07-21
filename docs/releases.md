---
title: Releases
description: Version tags, GHCR images, digest-pinned install.yaml, upgrades, and uninstall.
sidebarTitle: Releases
---

# Release and image versioning

The current public release is `v0.1.0-alpha.2`. Kontext releases use
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

## Install

Install a tagged release on an existing cluster without cloning the repository
or building images:

```bash
VERSION=v0.1.0-alpha.2
kubectl apply -f \
  "https://github.com/MFS-code/Kontext/releases/download/${VERSION}/install.yaml"
```

`install.yaml` contains the CRDs, Namespace, RBAC, controller Deployment,
webhook Service, NetworkPolicy, and narrow admission registration. It contains
no certificate or private key. The controller creates and rotates the
namespaced TLS Secret after installation. Its operator and trusted reporter
references use the immutable digests recorded in `image-digests.json`, while
`app.kubernetes.io/version` and `kontext.dev/release` retain the human-readable
release tag.

The registration matches only sparse referenced `AgentRun` CREATE requests.
It fails matching requests closed, uses a five-second timeout, and leaves
complete standalone and controller-created runs outside the webhook. Every
controller replica reconciles the shared TLS Secret and registration, reloads
renewed serving certificates, and must agree with the registered CA bundle
before becoming ready.

Webhook permissions are split from controller reconciliation and leader
election. A namespaced Role manages only the TLS Secret. A separate
ClusterRole manages only the named `MutatingWebhookConfiguration`, except that
Kubernetes RBAC cannot name-scope the create verb. The release NetworkPolicy
is ingress-only and selects controller Pods for webhook port 9443 and health
port 8081. It does not restrict `AgentRun` workloads or controller egress.

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

# Remove identities used by releases before the kontext- name prefix.
kubectl delete deployment/controller-manager \
  -n kontext-system --ignore-not-found=true --wait=true
kubectl delete \
  mutatingwebhookconfiguration/task-agentrun-mutator.kontext.dev \
  clusterrolebinding/manager-rolebinding \
  clusterrolebinding/webhook-registration-manager \
  clusterrole/manager-role \
  clusterrole/webhook-registration-manager \
  --ignore-not-found=true
kubectl delete \
  service/webhook-service \
  serviceaccount/controller-manager \
  rolebinding/leader-election-manager \
  rolebinding/webhook-certificate-manager \
  role/leader-election-manager \
  role/webhook-certificate-manager \
  networkpolicy/controller-manager-webhook \
  -n kontext-system --ignore-not-found=true

kubectl rollout status deployment/kontext-controller-manager \
  --namespace kontext-system \
  --timeout=180s
```

The explicit cleanup is idempotent. It stops an unprefixed controller from
retaining the shared leader-election Lease and removes its fail-closed webhook
before the old Service disappears. The namespaced TLS Secret remains and is
renewed for the prefixed webhook Service.

Kubernetes updates the CRDs and current resources declaratively; existing
`Agent` and `AgentRun` resources remain. Downgrades are not supported as an API
compatibility guarantee. Release CI nevertheless migrates in both directions,
applies the previous manifest after the candidate as a rollback safety check,
verifies baseline workloads remain available, and then restores the candidate.

## Uninstall

To remove only the Kontext control plane while retaining the CRDs and custom
resources in other namespaces:

```bash
kubectl delete clusterrolebinding kontext-manager-rolebinding \
  --ignore-not-found=true
kubectl delete clusterrole kontext-manager-role \
  --ignore-not-found=true
kubectl delete mutatingwebhookconfiguration \
  kontext-task-agentrun-mutator.kontext.dev --ignore-not-found=true
kubectl delete clusterrolebinding kontext-webhook-registration-manager \
  --ignore-not-found=true
kubectl delete clusterrole kontext-webhook-registration-manager \
  --ignore-not-found=true
kubectl delete mutatingwebhookconfiguration \
  task-agentrun-mutator.kontext.dev --ignore-not-found=true
kubectl delete clusterrolebinding manager-rolebinding \
  webhook-registration-manager --ignore-not-found=true
kubectl delete clusterrole manager-role \
  webhook-registration-manager --ignore-not-found=true
kubectl delete namespace kontext-system \
  --ignore-not-found=true --wait=true
```

Deleting `kontext-system` removes the controller and its ServiceAccount.
`Agent` and `AgentRun` resources in other namespaces remain stored and can be
reconciled again by reapplying an install manifest. Any custom resources
created inside `kontext-system` are deleted with that Namespace.

To remove Kontext completely, including both CRDs:

```bash
VERSION=v0.1.0-alpha.2
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
