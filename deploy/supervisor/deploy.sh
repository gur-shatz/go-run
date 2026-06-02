#!/usr/bin/env bash
# Deploy a packaged supervisor bundle to hertzner1.
#
# Env overrides:
#   BUNDLE=<path>
#   HOST=hertzner1
#   KUBECONFIG=~/.kube/hertzner1.yaml

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
KUBECONFIG_PATH="${KUBECONFIG:-$HOME/.kube/hertzner1.yaml}"
HOST="${HOST:-hertzner1}"
NAMESPACE="supervisor"

find_latest_bundle() {
  local latest=""
  local bundle
  while IFS= read -r -d '' bundle; do
    if [[ -z "$latest" || "$bundle" -nt "$latest" ]]; then
      latest="$bundle"
    fi
  done < <(find "$PROJECT_ROOT/build" -maxdepth 1 -type f -name 'supervisor-*.bundle.tar.gz' -print0 2>/dev/null)
  if [[ -z "$latest" ]]; then
    return 1
  fi
  printf '%s\n' "$latest"
}

if [[ $# -gt 1 ]]; then
  echo "usage: $0 [bundle.tar.gz]" >&2
  exit 1
fi

BUNDLE_PATH="${1:-${BUNDLE:-}}"
if [[ -z "$BUNDLE_PATH" ]]; then
  if ! BUNDLE_PATH="$(find_latest_bundle)"; then
    echo "no supervisor bundle found; run deploy/supervisor/package.sh first or set BUNDLE" >&2
    exit 1
  fi
fi

if [[ ! -f "$BUNDLE_PATH" ]]; then
  echo "bundle not found: $BUNDLE_PATH" >&2
  exit 1
fi

if ! command -v helm >/dev/null 2>&1; then
  echo "helm not installed or not on PATH" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "==> extract bundle: $BUNDLE_PATH"
tar -C "$TMP_DIR" -xzf "$BUNDLE_PATH"

BUNDLE_NAME="$(basename "$BUNDLE_PATH" .bundle.tar.gz)"
EXTRACTED_DIR="$TMP_DIR/$BUNDLE_NAME"

if [[ ! -f "$EXTRACTED_DIR/image.tar" || ! -d "$EXTRACTED_DIR/chart" || ! -f "$EXTRACTED_DIR/values.yaml" || ! -d "$EXTRACTED_DIR/global" ]]; then
  echo "bundle is missing required files: $BUNDLE_PATH" >&2
  exit 1
fi

echo "==> upload bundle to $HOST"
scp "$BUNDLE_PATH" "$HOST:/tmp/"

echo "==> import image and seed /kubestorage/globals/supervisor"
ssh "$HOST" "rm -rf /tmp/$BUNDLE_NAME && mkdir -p /tmp/$BUNDLE_NAME && tar -C /tmp -xzf /tmp/$(basename "$BUNDLE_PATH") && sudo k3s ctr images import /tmp/$BUNDLE_NAME/image.tar && sudo mkdir -p /kubestorage/globals/supervisor && sudo cp -R /tmp/$BUNDLE_NAME/global/. /kubestorage/globals/supervisor/ && sudo chown -R 65532:65532 /kubestorage/globals/supervisor && rm -rf /tmp/$BUNDLE_NAME /tmp/$(basename "$BUNDLE_PATH")"

echo "==> helm upgrade --install"
helm --kubeconfig "$KUBECONFIG_PATH" upgrade --install "$NAMESPACE" "$EXTRACTED_DIR/chart" \
  -n "$NAMESPACE" \
  --create-namespace \
  -f "$EXTRACTED_DIR/values.yaml" \
  --wait \
  --timeout 2m

echo "==> rollout status"
kubectl --kubeconfig "$KUBECONFIG_PATH" -n "$NAMESPACE" rollout status deployment/supervisor --timeout=2m

echo "==> done"
