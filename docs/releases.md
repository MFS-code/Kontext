# Release and image versioning

Kontext releases use SemVer-compatible git tags:

```text
vMAJOR.MINOR.PATCH
vMAJOR.MINOR.PATCH-PRERELEASE
```

Build metadata suffixes (`+...`) are not supported because they are not valid
OCI tag characters. The first public alpha can therefore use a tag such as
`v0.1.0-alpha.1`.

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

The unmaintained `runtimes/python-anthropic` migration example is not
published.

## API relationship

The git tag, GitHub release, and image tags identify one version of the Kontext
distribution. They do not change the Kubernetes API group/version: current
releases serve `kontext.dev/v1alpha1`, and maintained runtimes emit the
versioned event and result contracts documented in [`SPEC.md`](../SPEC.md).

`v1alpha1` is intentionally unstable. A new Kontext alpha release may make
breaking CRD, runtime-contract, or status-shape changes while retaining the
same Kubernetes API version. Consumers must read the release notes and follow
the documented upgrade procedure rather than treating matching `v1alpha1`
labels as compatibility guarantees.
