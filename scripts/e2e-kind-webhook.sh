#!/usr/bin/env bash
# Focused self-managed webhook TLS, fail-closed, and HA acceptance.
set -euo pipefail

# shellcheck source=scripts/lib/common.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/common.sh"

NAMESPACE="kontext-system"
DEPLOYMENT="controller-manager"
SECRET="webhook-server-cert"
WEBHOOK="task-agentrun-mutator.kontext.dev"
ECHO_IMAGE="${KONTEXT_ECHO_IMAGE:-kontext-echo:dev}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/kontext-webhook-e2e.XXXXXX")"
PORT_FORWARD_PIDS=()

cleanup() {
  for pid in "${PORT_FORWARD_PIDS[@]-}"; do
    kill "${pid}" >/dev/null 2>&1 || true
  done
  kubectl scale deployment "${DEPLOYMENT}" -n "${NAMESPACE}" --replicas=1 >/dev/null 2>&1 || true
  kubectl delete agentrun webhook-complete-bypass webhook-complete-broken-trust \
    -n default --ignore-not-found=true --wait=false >/dev/null 2>&1 || true
  rm -rf "${TMP_DIR}"
}
trap cleanup EXIT

need kubectl openssl curl

wait_for_jsonpath() {
  local resource="$1"
  local expression="$2"
  local expected="$3"
  local namespace="${4:-}"
  for _ in $(seq 1 120); do
    local value=""
    if [[ -n "${namespace}" ]]; then
      value="$(kubectl get "${resource}" -n "${namespace}" -o "jsonpath=${expression}" 2>/dev/null || true)"
    else
      value="$(kubectl get "${resource}" -o "jsonpath=${expression}" 2>/dev/null || true)"
    fi
    if [[ "${value}" == "${expected}" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for ${resource} ${expression}=${expected}" >&2
  return 1
}

secret_value() {
  kubectl get secret "${SECRET}" -n "${NAMESPACE}" -o "jsonpath={.data.$1}" | base64 --decode
}

leaf_fingerprint() {
  secret_value 'tls\.crt' | openssl x509 -noout -fingerprint -sha256
}

echo "==> verifying fresh trusted bootstrap"
kubectl rollout status deployment/"${DEPLOYMENT}" -n "${NAMESPACE}" --timeout=180s
kubectl get service webhook-service -n "${NAMESPACE}" >/dev/null
kubectl get mutatingwebhookconfiguration "${WEBHOOK}" >/dev/null
kubectl get secret "${SECRET}" -n "${NAMESPACE}" >/dev/null
secret_value 'ca\.crt' >"${TMP_DIR}/ca.crt"
secret_value 'tls\.crt' >"${TMP_DIR}/tls.crt"
openssl verify -CAfile "${TMP_DIR}/ca.crt" "${TMP_DIR}/tls.crt"
openssl x509 -in "${TMP_DIR}/tls.crt" -noout -checkhost webhook-service.kontext-system.svc
secret_ca="$(kubectl get secret "${SECRET}" -n "${NAMESPACE}" -o jsonpath='{.data.ca\.crt}')"
registered_ca="$(kubectl get mutatingwebhookconfiguration "${WEBHOOK}" -o jsonpath='{.webhooks[0].clientConfig.caBundle}')"
if [[ "${secret_ca}" != "${registered_ca}" ]]; then
  echo "admission registration does not trust the Secret CA" >&2
  exit 1
fi

echo "==> proving sparse validation and nonmatching bypass"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: webhook-complete-bypass
  namespace: default
spec:
  goal: Complete standalone bypass
  model: echo-model
  runtime:
    image: ${ECHO_IMAGE}
EOF
if kubectl create -f - >"${TMP_DIR}/sparse.out" 2>&1 <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: webhook-sparse-schema-rejected
  namespace: default
spec:
  agentRef:
    name: reserved-task
EOF
then
  echo "sparse AgentRun unexpectedly persisted without #84" >&2
  exit 1
fi
if ! grep -Eq 'Required value|must contain a complete execution snapshot' "${TMP_DIR}/sparse.out"; then
  echo "sparse request did not reach schema validation through trusted TLS" >&2
  cat "${TMP_DIR}/sparse.out" >&2
  exit 1
fi

echo "==> proving fail-closed matching and bypass under broken trust"
kubectl patch mutatingwebhookconfiguration "${WEBHOOK}" --type=json \
  -p='[{"op":"replace","path":"/webhooks/0/clientConfig/caBundle","value":"aW52YWxpZA=="}]'
readiness_pod="$(
  kubectl get pods -n "${NAMESPACE}" -l control-plane=controller-manager \
    -o jsonpath='{.items[0].metadata.name}'
)"
kubectl port-forward -n "${NAMESPACE}" "pod/${readiness_pod}" 18081:8081 \
  >"${TMP_DIR}/readiness-port-forward.log" 2>&1 &
PORT_FORWARD_PIDS+=("$!")
readiness_code=""
for _ in $(seq 1 50); do
  readiness_code="$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:18081/readyz || true)"
  [[ "${readiness_code}" != "000" ]] && break
  sleep 0.1
done
if [[ "${readiness_code}" != "500" ]]; then
  echo "readiness did not fail while API-server trust disagreed: HTTP ${readiness_code}" >&2
  exit 1
fi
if kubectl create -f - >"${TMP_DIR}/untrusted.out" 2>&1 <<'EOF'
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: webhook-sparse-untrusted
  namespace: default
spec:
  agentRef:
    name: reserved-task
EOF
then
  echo "matching sparse request did not fail closed" >&2
  exit 1
fi
grep -q 'failed calling webhook' "${TMP_DIR}/untrusted.out"
cat <<EOF | kubectl apply -f -
apiVersion: kontext.dev/v1alpha1
kind: AgentRun
metadata:
  name: webhook-complete-broken-trust
  namespace: default
spec:
  goal: Complete bypass under broken webhook trust
  model: echo-model
  runtime:
    image: ${ECHO_IMAGE}
EOF
for _ in $(seq 1 90); do
  repaired_ca="$(kubectl get mutatingwebhookconfiguration "${WEBHOOK}" -o jsonpath='{.webhooks[0].clientConfig.caBundle}')"
  [[ "${repaired_ca}" == "${secret_ca}" ]] && break
  sleep 1
done
[[ "${repaired_ca}" == "${secret_ca}" ]]

echo "==> forcing near-expiry renewal"
old_fingerprint="$(leaf_fingerprint)"
secret_value 'ca\.key' >"${TMP_DIR}/ca.key"
openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout "${TMP_DIR}/near.key" -out "${TMP_DIR}/near.csr" \
  -subj '/CN=webhook-service.kontext-system.svc' >/dev/null 2>&1
cat >"${TMP_DIR}/san.ext" <<'EOF'
subjectAltName=DNS:webhook-service,DNS:webhook-service.kontext-system,DNS:webhook-service.kontext-system.svc,DNS:webhook-service.kontext-system.svc.cluster.local
extendedKeyUsage=serverAuth
keyUsage=digitalSignature
EOF
openssl x509 -req -in "${TMP_DIR}/near.csr" \
  -CA "${TMP_DIR}/ca.crt" -CAkey "${TMP_DIR}/ca.key" -CAcreateserial \
  -days 1 -extfile "${TMP_DIR}/san.ext" -out "${TMP_DIR}/near.crt" >/dev/null 2>&1
near_cert="$(base64 <"${TMP_DIR}/near.crt" | tr -d '\n')"
near_key="$(base64 <"${TMP_DIR}/near.key" | tr -d '\n')"
kubectl patch secret "${SECRET}" -n "${NAMESPACE}" --type=json -p="$(
  printf '[{"op":"replace","path":"/data/tls.crt","value":"%s"},{"op":"replace","path":"/data/tls.key","value":"%s"}]' \
    "${near_cert}" "${near_key}"
)"
near_fingerprint="$(openssl x509 -in "${TMP_DIR}/near.crt" -noout -fingerprint -sha256)"
for _ in $(seq 1 120); do
  new_fingerprint="$(leaf_fingerprint)"
  [[ "${new_fingerprint}" != "${old_fingerprint}" && "${new_fingerprint}" != "${near_fingerprint}" ]] && break
  sleep 1
done
if [[ "${new_fingerprint}" == "${old_fingerprint}" || "${new_fingerprint}" == "${near_fingerprint}" ]]; then
  echo "near-expiry certificate was not renewed" >&2
  exit 1
fi

echo "==> verifying restart reuse"
renewed_fingerprint="${new_fingerprint}"
kubectl rollout restart deployment/"${DEPLOYMENT}" -n "${NAMESPACE}"
kubectl rollout status deployment/"${DEPLOYMENT}" -n "${NAMESPACE}" --timeout=180s
if [[ "$(leaf_fingerprint)" != "${renewed_fingerprint}" ]]; then
  echo "controller restart replaced valid shared certificate" >&2
  exit 1
fi

echo "==> verifying two-replica convergence"
kubectl scale deployment "${DEPLOYMENT}" -n "${NAMESPACE}" --replicas=2
kubectl rollout status deployment/"${DEPLOYMENT}" -n "${NAMESPACE}" --timeout=180s
wait_for_jsonpath "deployment/${DEPLOYMENT}" '{.status.readyReplicas}' '2' "${NAMESPACE}"
pods=()
while IFS= read -r pod; do
  [[ -n "${pod}" ]] && pods+=("${pod}")
done < <(
  kubectl get pods -n "${NAMESPACE}" -l control-plane=controller-manager \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}'
)
if [[ "${#pods[@]}" -ne 2 ]]; then
  echo "expected two controller replicas, found ${#pods[@]}" >&2
  exit 1
fi
fingerprints=()
for index in 0 1; do
  port="$((19443 + index))"
  kubectl port-forward -n "${NAMESPACE}" "pod/${pods[index]}" "${port}:9443" \
    >"${TMP_DIR}/port-forward-${index}.log" 2>&1 &
  PORT_FORWARD_PIDS+=("$!")
  for _ in $(seq 1 50); do
    if fingerprint="$(
      openssl s_client -connect "127.0.0.1:${port}" \
        -servername webhook-service.kontext-system.svc </dev/null 2>/dev/null |
        openssl x509 -noout -fingerprint -sha256 2>/dev/null
    )"; then
      fingerprints+=("${fingerprint}")
      break
    fi
    sleep 0.2
  done
done
if [[ "${#fingerprints[@]}" -ne 2 || "${fingerprints[0]}" != "${fingerprints[1]}" ]]; then
  echo "two replicas do not serve the same shared certificate" >&2
  exit 1
fi

echo "focused webhook TLS and HA acceptance passed"
