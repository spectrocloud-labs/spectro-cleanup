#!/usr/bin/env bash
#
# Smoke tests for spectro-cleanup on a kind cluster.
#
# Scenarios:
#   happy  A Job deletes two canary resources via resource-config.json.
#          No --self-* flags are set, so the Job is left around for the
#          caller (e.g. a Helm hook) to clean up.
#   error  --self-name points at a non-existent target, so setOwnerReferences
#          fails. Exercises the structured error log with gvr/name/namespace.
#   grpc   Pod with --enable-grpc-server and --blocking-deletion=false.
#          Processes resource-config.json, then holds in runSelfCleanup
#          until a FinalizeCleanup request arrives. On receipt, deletes
#          itself via the --self-* flags.
#
# Usage:
#   ./hack/smoke/run.sh --image <image> [--cluster <name>]
#                       [--pull-policy <IfNotPresent|Always|Never>]
#                       [--keep] [--no-cluster] [--load]
#                       [--scenario happy|error|grpc|all]
#
# Examples:
#   # Locally built dev image, fresh cluster, both scenarios.
#   ./hack/smoke/run.sh --image quay.io/spectrocloud-labs/spectro-cleanup:dev --load
#
#   # Published image; kind pulls from the registry.
#   ./hack/smoke/run.sh --image ghcr.io/example/spectro-cleanup:v1.8.0 \
#                       --pull-policy Always
#
#   # Reuse an existing cluster, keep it after the run for inspection.
#   ./hack/smoke/run.sh --image my/img:tag --no-cluster --keep
#
# Environment variables (overridden by flags):
#   IMG                  image reference to test
#   CLUSTER              kind cluster name (default: spectro-cleanup-smoke)
#   IMAGE_PULL_POLICY    pull policy stamped into the manifests
#
# Prerequisites: docker, kind, kubectl, curl, envsubst.
set -euo pipefail
set +m   # disable bash job-control messages so backgrounded port-forwards
         # don't print "Terminated" on cleanup

CLUSTER="${CLUSTER:-spectro-cleanup-smoke}"
IMG="${IMG:-}"
IMAGE_PULL_POLICY="${IMAGE_PULL_POLICY:-IfNotPresent}"
SCENARIO="all"
KEEP=false
NO_CLUSTER=false
LOAD=false

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -t 1 ]]; then
    C_BOLD=$'\033[1m'
    C_GREEN=$'\033[32m'
    C_DIM=$'\033[2m'
    C_RESET=$'\033[0m'
else
    C_BOLD='' C_GREEN='' C_DIM='' C_RESET=''
fi

usage() {
    grep -E '^# ' "$0" | sed 's/^# \?//'
    exit "${1:-0}"
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --image)        IMG="$2"; shift 2 ;;
        --cluster)      CLUSTER="$2"; shift 2 ;;
        --pull-policy)  IMAGE_PULL_POLICY="$2"; shift 2 ;;
        --scenario)     SCENARIO="$2"; shift 2 ;;
        --keep)         KEEP=true; shift ;;
        --no-cluster)   NO_CLUSTER=true; shift ;;
        --load)         LOAD=true; shift ;;
        -h|--help)      usage ;;
        *)              echo "unknown flag: $1" >&2; usage 1 ;;
    esac
done

if [[ -z "${IMG}" ]]; then
    echo "ERROR: --image (or IMG env var) is required" >&2
    usage 1
fi

for cmd in docker kind kubectl curl envsubst; do
    command -v "${cmd}" >/dev/null || {
        echo "missing required tool: ${cmd}" >&2
        exit 1
    }
done

export IMG IMAGE_PULL_POLICY

heading() {
    printf '\n%s===========================================================%s\n' "${C_BOLD}" "${C_RESET}"
    printf '%s  %s%s\n' "${C_BOLD}" "$1" "${C_RESET}"
    printf '%s===========================================================%s\n' "${C_BOLD}" "${C_RESET}"
}

step() {
    printf '\n%s>>%s %s\n' "${C_BOLD}" "${C_RESET}" "$1"
}

note() {
    printf '%s%s%s\n' "${C_DIM}" "$1" "${C_RESET}"
}

ok() {
    printf '%s%s%s\n' "${C_GREEN}" "$1" "${C_RESET}"
}

apply_manifest() {
    note "applying $1"
    envsubst < "${SCRIPT_DIR}/$1" | kubectl apply -f - >/dev/null
}

cleanup() {
    if [[ "${KEEP}" == "true" ]]; then
        echo
        note "Cluster '${CLUSTER}' kept (use 'kind delete cluster --name ${CLUSTER}' to remove)."
        return
    fi
    if [[ "${NO_CLUSTER}" == "true" ]]; then
        return
    fi
    kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

#-----------------------------------------------------------------------------
if [[ "${NO_CLUSTER}" != "true" ]]; then
    heading "Setting up kind cluster '${CLUSTER}'"
    kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
    kind create cluster --name "${CLUSTER}" 2>&1 | sed 's/^/  /'
fi

if [[ "${LOAD}" == "true" ]]; then
    step "Loading image ${IMG} into kind"
    kind load docker-image "${IMG}" --name "${CLUSTER}" 2>&1 | sed 's/^/  /'
fi

#-----------------------------------------------------------------------------
if [[ "${SCENARIO}" == "happy" || "${SCENARIO}" == "all" ]]; then
    heading "Scenario: happy"
    note "Job deletes a canary ConfigMap and Secret, then exits cleanly."

    apply_manifest happy-path.yaml

    step "Waiting for Job to complete"
    kubectl wait --for=condition=Complete job/spectro-cleanup-happy \
        -n spectro-happy --timeout=120s | sed 's/^/  /'

    step "Pod logs"
    kubectl logs -n spectro-happy job/spectro-cleanup-happy | sed 's/^/  /'

    step "Verifying canary resources are gone"
    kubectl get cm,secret -n victim 2>&1 | sed 's/^/  /'

    step "Job stays for inspection"
    kubectl get job -n spectro-happy 2>&1 | sed 's/^/  /'
    ok "PASS: canaries deleted, Job condition=Complete"
fi

#-----------------------------------------------------------------------------
if [[ "${SCENARIO}" == "error" || "${SCENARIO}" == "all" ]]; then
    heading "Scenario: error"
    note "--self-name points at a non-existent target. The Get inside"
    note "setOwnerReferences should fail with a structured error."

    apply_manifest error.yaml

    step "Waiting for Job to fail"
    kubectl wait --for=condition=Failed job/spectro-cleanup-bad-self \
        -n spectro-test --timeout=60s 2>&1 | sed 's/^/  /' || true

    step "Pod logs"
    kubectl logs -n spectro-test job/spectro-cleanup-bad-self 2>&1 | sed 's/^/  /'

    step "Expected fields in the error line"
    cat <<EOF | sed 's/^/  /'
"gvr":"batch/v1, Resource=jobs"
"name":"does-not-exist"
"namespace":"spectro-test"
"message":"failed to get owner resource for setOwnerReferences"
EOF
    ok "PASS: structured error logged with gvr/name/namespace context"
fi

#-----------------------------------------------------------------------------
if [[ "${SCENARIO}" == "grpc" || "${SCENARIO}" == "all" ]]; then
    heading "Scenario: grpc"
    note "Pod runs the gRPC server, deletes canary resources, holds for a"
    note "FinalizeCleanup call, then self-deletes via the --self-* flags."

    apply_manifest grpc-self-cleanup.yaml

    step "Waiting for Pod to be Ready"
    kubectl wait --for=condition=Ready pod/spectro-cleanup-grpc \
        -n spectro-grpc --timeout=60s | sed 's/^/  /'

    step "Tailing pod logs to /tmp/spectro-cleanup-grpc.log"
    : > /tmp/spectro-cleanup-grpc.log
    kubectl logs -f -n spectro-grpc pod/spectro-cleanup-grpc \
        >/tmp/spectro-cleanup-grpc.log 2>&1 &
    LOG_PID=$!
    disown "${LOG_PID}" 2>/dev/null || true

    step "Port-forwarding pod :8080 -> localhost:18080"
    kubectl port-forward -n spectro-grpc pod/spectro-cleanup-grpc \
        18080:8080 >/tmp/spectro-cleanup-pf.log 2>&1 &
    PF_PID=$!
    disown "${PF_PID}" 2>/dev/null || true
    sleep 2

    step "Sending FinalizeCleanup"
    curl --http2-prior-knowledge -sS -X POST \
        -H "Content-Type: application/json" \
        -d '{}' \
        "http://localhost:18080/cleanup.v1.CleanupService/FinalizeCleanup" | sed 's/^/  /'
    echo

    step "Waiting for Pod to self-destruct"
    kubectl wait --for=delete pod/spectro-cleanup-grpc \
        -n spectro-grpc --timeout=60s 2>&1 | sed 's/^/  /' || true

    kill "${PF_PID}" 2>/dev/null || true
    kill "${LOG_PID}" 2>/dev/null || true
    wait "${PF_PID}" 2>/dev/null || true
    wait "${LOG_PID}" 2>/dev/null || true

    step "Pod logs (captured before self-destruction)"
    sed 's/^/  /' /tmp/spectro-cleanup-grpc.log

    step "Verifying end state"
    {
        echo "canary resources (expected: only kube-root-ca.crt):"
        kubectl get cm,secret -n victim-grpc 2>&1
        echo
        echo "spectro-grpc namespace (expected: no Pod):"
        kubectl get pods -n spectro-grpc 2>&1
    } | sed 's/^/  /'
    ok "PASS: FinalizeCleanup processed, canaries gone, Pod self-deleted"
fi
