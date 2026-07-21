#!/usr/bin/env bash
# Shared shell utilities and canonical Kontext repository/registry identity.

if [[ "${_KONTEXT_COMMON_LOADED:-}" == "true" ]]; then
  return 0
fi
readonly _KONTEXT_COMMON_LOADED="true"

readonly KONTEXT_GITHUB_OWNER="MFS-code"
readonly KONTEXT_REPOSITORY_NAME="Kontext"
readonly KONTEXT_GITHUB_REPOSITORY="${KONTEXT_GITHUB_OWNER}/${KONTEXT_REPOSITORY_NAME}"
readonly KONTEXT_REGISTRY="ghcr.io"
readonly KONTEXT_REGISTRY_OWNER="mfs-code"
readonly KONTEXT_IMAGE_REPOSITORY="${KONTEXT_REGISTRY}/${KONTEXT_REGISTRY_OWNER}"

repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd
}

need() {
  local command
  for command in "$@"; do
    if ! command -v "${command}" >/dev/null 2>&1; then
      echo "missing required command: ${command}" >&2
      exit 1
    fi
  done
}

validate_release_tag() {
  local tag="${1:-}"
  local core='v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)'
  local prerelease_id='(0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
  local release_pattern="^${core}(-${prerelease_id}(\.${prerelease_id})*)?$"

  [[ "${tag}" =~ ${release_pattern} && "${#tag}" -le 63 ]]
}

wait_until() {
  local attempts="${1:-}"
  local interval_seconds="${2:-}"
  local description="${3:-}"
  local attempt

  if [[ ! "${attempts}" =~ ^[1-9][0-9]*$ ]]; then
    echo "poll attempts must be a positive whole number: ${attempts}" >&2
    return 2
  fi
  if [[ ! "${interval_seconds}" =~ ^[0-9]+([.][0-9]+)?$ ||
    "${interval_seconds}" =~ ^0+([.]0+)?$ ]]; then
    echo "poll interval must be a positive number of seconds: ${interval_seconds}" >&2
    return 2
  fi
  if [[ -z "${description}" || "$#" -lt 4 ]]; then
    echo "usage: wait_until <attempts> <interval_seconds> <description> cmd args..." >&2
    return 2
  fi
  shift 3

  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if "$@"; then
      return 0
    fi
    if ((attempt < attempts)); then
      sleep "${interval_seconds}"
    fi
  done

  echo "timed out waiting for ${description} after ${attempts} attempts" >&2
  return 1
}

wait_for_run_phase() {
  local name="${1:?AgentRun name is required}"
  local expected="${2:?expected AgentRun phase is required}"
  local namespace="${3:-default}"
  local timeout_seconds="${4:-120}"
  local poll_seconds="${5:-2}"
  local elapsed=0
  local phase=""

  if [[ ! "${timeout_seconds}" =~ ^[1-9][0-9]*$ ]]; then
    echo "AgentRun wait timeout must be a positive whole number of seconds: ${timeout_seconds}" >&2
    return 2
  fi
  if [[ ! "${poll_seconds}" =~ ^[1-9][0-9]*$ ]]; then
    echo "AgentRun poll interval must be a positive whole number of seconds: ${poll_seconds}" >&2
    return 2
  fi

  while ((elapsed < timeout_seconds)); do
    phase="$(
      kubectl get agentrun "${name}" -n "${namespace}" \
        -o jsonpath='{.status.phase}' 2>/dev/null || true
    )"
    if [[ "${phase}" == "${expected}" ]]; then
      return 0
    fi
    case "${phase}" in
      ""|Pending|Running) ;;
      *)
        echo "expected ${namespace}/${name} phase=${expected}, got ${phase}" >&2
        return 1
        ;;
    esac
    sleep "${poll_seconds}"
    elapsed=$((elapsed + poll_seconds))
  done

  echo "${namespace}/${name} did not reach ${expected}; last phase=${phase}" >&2
  return 1
}

write_development_overlay() {
  local output_dir="${1:?overlay output directory is required}"
  local operator_image="${2:?operator image is required}"
  local reporter_image="${3:?reporter image is required}"
  local operator_image_id="${4:-}"
  local root
  root="$(repo_root)"

  cp -R "${root}/config" "${output_dir}/config"

  cat >"${output_dir}/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - config/overlays/dev

patches:
  - path: manager_patch.yaml
EOF

  cat >"${output_dir}/manager_patch.yaml" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kontext-controller-manager
  namespace: kontext-system
spec:
  template:
EOF
  if [[ -n "${operator_image_id}" ]]; then
    cat >>"${output_dir}/manager_patch.yaml" <<EOF
    metadata:
      annotations:
        kontext.dev/local-operator-image-id: "${operator_image_id}"
EOF
  fi
  cat >>"${output_dir}/manager_patch.yaml" <<EOF
    spec:
      containers:
        - name: manager
          image: "${operator_image}"
          env:
            - name: KONTEXT_REPORTER_IMAGE
              value: "${reporter_image}"
EOF
}

kontext_image() {
  local package="${1:?image package is required}"
  printf '%s/%s' "${KONTEXT_IMAGE_REPOSITORY}" "${package}"
}
