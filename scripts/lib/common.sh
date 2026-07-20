#!/usr/bin/env bash
# Shared, release-relevant Kontext repository and registry identity.

readonly KONTEXT_GITHUB_OWNER="MFS-code"
readonly KONTEXT_REPOSITORY_NAME="Kontext"
readonly KONTEXT_GITHUB_REPOSITORY="${KONTEXT_GITHUB_OWNER}/${KONTEXT_REPOSITORY_NAME}"
readonly KONTEXT_REGISTRY="ghcr.io"
readonly KONTEXT_REGISTRY_OWNER="mfs-code"
readonly KONTEXT_IMAGE_REPOSITORY="${KONTEXT_REGISTRY}/${KONTEXT_REGISTRY_OWNER}"

kontext_image() {
  local package="${1:?image package is required}"
  printf '%s/%s' "${KONTEXT_IMAGE_REPOSITORY}" "${package}"
}
