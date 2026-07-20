#!/usr/bin/env bash
# Check that release tooling consumes the canonical repository and registry identity.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib/common.sh
source "${ROOT_DIR}/scripts/lib/common.sh"

fail() {
  echo "release identity test failed: $*" >&2
  exit 1
}

assert_workflow_registry_owner() {
  local file="$1"
  local expected='echo "registry_owner=${KONTEXT_REGISTRY_OWNER}" >>"${GITHUB_OUTPUT}"'
  local assignments=0
  local line
  local trimmed

  while IFS= read -r line || [[ -n "${line}" ]]; do
    trimmed="${line#"${line%%[![:space:]]*}"}"
    case "${trimmed}" in
      'echo "registry_owner='*)
        assignments=$((assignments + 1))
        [[ "${trimmed}" == "${expected}" ]] ||
          fail "${file} has an unexpected registry_owner output assignment"
        ;;
    esac
  done <"${file}"
  [[ "${assignments}" -eq 1 ]] ||
    fail "${file} must assign registry_owner exactly once"
}

# Re-sourcing must preserve the original readonly identity without aborting.
source "${ROOT_DIR}/scripts/lib/common.sh"
[[ "${KONTEXT_GITHUB_REPOSITORY}" == "MFS-code/Kontext" ]] ||
  fail "unexpected GitHub repository ${KONTEXT_GITHUB_REPOSITORY}"
[[ "${KONTEXT_IMAGE_REPOSITORY}" == "ghcr.io/mfs-code" ]] ||
  fail "unexpected image repository ${KONTEXT_IMAGE_REPOSITORY}"
if (KONTEXT_GITHUB_OWNER="other-owner") 2>/dev/null; then
  fail "canonical identity is mutable after re-sourcing"
fi

workflow="${ROOT_DIR}/.github/workflows/release.yml"
grep -Fq "source scripts/lib/common.sh" "${workflow}" ||
  fail "release workflow does not source common identity"
assert_workflow_registry_owner "${workflow}"

for script in \
  "${ROOT_DIR}/scripts/render-release-manifest.sh" \
  "${ROOT_DIR}/scripts/apply-example.sh" \
  "${ROOT_DIR}/scripts/verify-release-install.sh"; do
  grep -Fq 'source "${ROOT_DIR}/scripts/lib/common.sh"' "${script}" ||
    fail "${script#${ROOT_DIR}/} does not source common identity"
  if grep -Fq "ghcr.io/mfs-code" "${script}"; then
    fail "${script#${ROOT_DIR}/} duplicates the registry owner"
  fi
done

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/kontext-release-identity.XXXXXX")"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

workflow_with_comment="${tmp_dir}/release-with-comment.yml"
cp "${workflow}" "${workflow_with_comment}"
printf '%s\n' \
  "# GITHUB_REPOSITORY_OWNER is intentionally not used for release images." \
  >>"${workflow_with_comment}"
assert_workflow_registry_owner "${workflow_with_comment}"

cat >"${tmp_dir}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" == "kustomize" ]] || exit 2
directory="${2:?kustomize directory is required}"
file="${directory}/manager_patch.yaml"
if [[ ! -f "${file}" ]]; then
  file="${directory}/kustomization.yaml"
fi
while IFS= read -r line || [[ -n "${line}" ]]; do
  printf '%s\n' "${line}"
done <"${file}"
EOF
chmod +x "${tmp_dir}/kubectl"

metadata="${ROOT_DIR}/scripts/testdata/release-image-digests.json"
PATH="${tmp_dir}:${PATH}" \
  "${ROOT_DIR}/scripts/render-release-manifest.sh" "${metadata}" \
  >"${tmp_dir}/install.yaml"
grep -Fq "$(kontext_image kontext-operator)@sha256:" "${tmp_dir}/install.yaml" ||
  fail "rendered manifest lacks canonical operator image"
grep -Fq "$(kontext_image kontext-reporter)@sha256:" "${tmp_dir}/install.yaml" ||
  fail "rendered manifest lacks canonical reporter image"

jq --arg wrong_repository "ghcr.io/other-owner" \
  '(.images[] | select(.name == "operator").immutableReference) |=
    (. as $reference | $wrong_repository + "/" + ($reference | split("/") | last))' \
  "${metadata}" >"${tmp_dir}/wrong-owner.json"
operator_suffix="$(
  jq -er '.images[] | select(.name == "operator").immutableReference | split("/") | last' \
    "${metadata}"
)"
wrong_operator_reference="$(
  jq -er '.images[] | select(.name == "operator").immutableReference' \
    "${tmp_dir}/wrong-owner.json"
)"
[[ "${wrong_operator_reference}" == "ghcr.io/other-owner/${operator_suffix}" ]] ||
  fail "wrong-owner fixture was not updated structurally"
if PATH="${tmp_dir}:${PATH}" \
  "${ROOT_DIR}/scripts/render-release-manifest.sh" "${tmp_dir}/wrong-owner.json" \
  >"${tmp_dir}/wrong-owner.yaml" 2>/dev/null; then
  fail "renderer accepted an image from another registry owner"
fi

PATH="${tmp_dir}:${PATH}" \
  KONTEXT_RELEASE_TAG=v0.0.0-test.1 \
  KONTEXT_RENDER_ONLY=true \
  "${ROOT_DIR}/scripts/apply-example.sh" echo-task-run.yaml \
  >"${tmp_dir}/example.yaml"
grep -Fq "name: $(kontext_image kontext-echo)" "${tmp_dir}/example.yaml" ||
  fail "example renderer lacks canonical source image"
grep -Fq "newName: $(kontext_image kontext-echo)" "${tmp_dir}/example.yaml" ||
  fail "example renderer lacks canonical release image"

echo "release identity test passed"
