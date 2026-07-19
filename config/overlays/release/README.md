# Release overlay

This is the stable base for generated release artifacts. Do not apply it
directly: the manager base still contains logical `controller:latest` and
`reporter:latest` placeholders.

`scripts/render-release-manifest.sh` layers the release tag and immutable
operator/reporter digests from `image-digests.json`, then emits the complete
install manifest.
