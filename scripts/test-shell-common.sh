#!/usr/bin/env bash
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"
ROOT_DIR="$(repo_root)"

fail() {
  echo "shell regression test failed: $*" >&2
  exit 1
}

[[ "${ROOT_DIR}" == "$(git -C "${ROOT_DIR}" rev-parse --show-toplevel)" ]] ||
  fail "repo_root did not resolve the checkout root"

for tag in v0.0.0 v1.2.3 v10.20.30-rc.1 v1.0.0-alpha-1; do
  validate_release_tag "${tag}" || fail "valid release tag was rejected: ${tag}"
done
for tag in 1.2.3 v01.2.3 v1.02.3 v1.2 v1.2.3-01 "v1.2.3+build"; do
  if validate_release_tag "${tag}"; then
    fail "invalid release tag was accepted: ${tag}"
  fi
done
long_tag="v1.2.3-$(printf 'a%.0s' {1..57})"
if validate_release_tag "${long_tag}"; then
  fail "release tag longer than 63 characters was accepted"
fi

need sh
if (need kontext-command-that-does-not-exist) 2>/dev/null; then
  fail "need accepted a missing command"
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/kontext-shell-test.XXXXXX")"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

cat >"${tmp_dir}/kubectl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"${KUBECTL_ARGS_FILE}"
printf '%s' "${KUBECTL_PHASE}"
EOF
chmod +x "${tmp_dir}/kubectl"

KUBECTL_ARGS_FILE="${tmp_dir}/kubectl.args" \
  KUBECTL_PHASE=Succeeded \
  PATH="${tmp_dir}:${PATH}" \
  wait_for_run_phase test-run Succeeded test-namespace 1 1
[[ "$(<"${tmp_dir}/kubectl.args")" == \
  "get agentrun test-run -n test-namespace -o jsonpath={.status.phase}" ]] ||
  fail "wait_for_run_phase did not pass the requested namespace"
if KUBECTL_ARGS_FILE="${tmp_dir}/kubectl.args" \
  KUBECTL_PHASE=Failed \
  PATH="${tmp_dir}:${PATH}" \
  wait_for_run_phase test-run Succeeded test-namespace 1 1 2>/dev/null; then
  fail "wait_for_run_phase accepted an unexpected terminal phase"
fi
if wait_for_run_phase test-run Succeeded test-namespace invalid 1 2>/dev/null; then
  fail "wait_for_run_phase accepted an invalid timeout"
fi

overlay="${tmp_dir}/overlay"
mkdir "${overlay}"
write_development_overlay "${overlay}" operator:test reporter:test
[[ -f "${overlay}/config/default/kustomization.yaml" ]] ||
  fail "development overlay did not copy config"
if grep -Fq "local-operator-image-id" "${overlay}/manager_patch.yaml"; then
  fail "development overlay emitted an empty local image annotation"
fi
grep -Fq 'image: "operator:test"' "${overlay}/manager_patch.yaml" ||
  fail "development overlay omitted the operator image"
grep -Fq 'value: "reporter:test"' "${overlay}/manager_patch.yaml" ||
  fail "development overlay omitted the reporter image"

overlay_with_id="${tmp_dir}/overlay-with-id"
mkdir "${overlay_with_id}"
write_development_overlay \
  "${overlay_with_id}" operator:test reporter:test sha256:local
grep -Fq 'kontext.dev/local-operator-image-id: "sha256:local"' \
  "${overlay_with_id}/manager_patch.yaml" ||
  fail "development overlay omitted the local image annotation"

cat >"${tmp_dir}/tools.jsonl" <<'EOF'
not-json
{"type":"tool","data":{"name":"first","outputBytes":10,"isError":false,"truncated":false}}
{"type":"tool","data":{"name":"second","outputBytes":20,"isError":true,"truncated":true}}
EOF
"${ROOT_DIR}/scripts/validators/validate-tool-events.py" \
  "${tmp_dir}/tools.jsonl" 2 20 30 first,second 1 1
if "${ROOT_DIR}/scripts/validators/validate-tool-events.py" \
  "${tmp_dir}/tools.jsonl" 1 20 30 first 0 0 2>/dev/null; then
  fail "tool-event validator accepted the wrong event count"
fi

cat >"${tmp_dir}/browser-pod.json" <<'EOF'
{
  "spec": {
    "automountServiceAccountToken": false,
    "containers": [{
      "name": "mcp",
      "env": [
        {"name": "HOME", "value": "/tmp/home"},
        {"name": "TMPDIR", "value": "/tmp"},
        {"name": "XDG_CACHE_HOME", "value": "/tmp/xdg-cache"},
        {"name": "XDG_CONFIG_HOME", "value": "/tmp/xdg-config"},
        {"name": "PLAYWRIGHT_MCP_PING_TIMEOUT_MS", "value": "30000"}
      ],
      "volumeMounts": [
        {"name": "tmp", "mountPath": "/tmp"},
        {"name": "dev-shm", "mountPath": "/dev/shm"}
      ]
    }],
    "volumes": [
      {"name": "tmp", "emptyDir": {}},
      {"name": "dev-shm", "emptyDir": {}}
    ]
  }
}
EOF
"${ROOT_DIR}/scripts/validators/validate-pod-isolation.py" \
  "${tmp_dir}/browser-pod.json" mcp browser

echo "shell regression tests passed"
