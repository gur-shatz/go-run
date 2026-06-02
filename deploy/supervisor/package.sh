#!/usr/bin/env bash
# Build a self-contained supervisor deployment bundle for hertzner1.
#
# The bundle contains:
#   - image.tar                         OCI image tarball produced by ko
#   - chart/                            frozen Helm chart
#   - values.yaml                       values with image tag/domains injected
#   - global/supervisor/config.yml      persistent supervisor config seed
#   - global/origin/hello/...           hello component update tree
#   - manifest.json                     bundle metadata
#
# Env overrides:
#   PLATFORM=linux/arm64
#   IMAGE_TAG=<tag>
#   BACKOFFICE_DOMAIN=supervisor.203.0.113.10.nip.io
#   PUBLIC_DOMAIN=hello.203.0.113.10.nip.io
#   HELLO_VERSION=v1

set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DEPLOY_DIR="$PROJECT_ROOT/deploy/supervisor"
BUILD_DIR="$PROJECT_ROOT/build"
PLATFORM="${PLATFORM:-linux/arm64}"
IMAGE_REPO="localhost/supervisor"
BACKOFFICE_DOMAIN="${BACKOFFICE_DOMAIN:-supervisor.203.0.113.10.nip.io}"
PUBLIC_DOMAIN="${PUBLIC_DOMAIN:-hello.203.0.113.10.nip.io}"
HELLO_VERSION="${HELLO_VERSION:-v1}"

case "$PLATFORM" in
  */*) HELLO_GOOS="${PLATFORM%%/*}"; HELLO_GOARCH="${PLATFORM##*/}" ;;
  *) echo "PLATFORM must be os/arch, got: $PLATFORM" >&2; exit 1 ;;
esac

mkdir -p "$BUILD_DIR"

cd "$PROJECT_ROOT"

if ! command -v ko >/dev/null 2>&1; then
  echo "ko not installed. Install: go install github.com/google/ko@latest" >&2
  exit 1
fi

if [[ -n "${IMAGE_TAG:-}" ]]; then
  TAG="$IMAGE_TAG"
else
  SHA="$(git rev-parse --short HEAD)"
  if ! git diff --quiet HEAD 2>/dev/null; then
    SHA="${SHA}-dirty-$(date +%s)"
  fi
  TAG="$SHA"
fi

ARCH="$HELLO_GOARCH"
BUNDLE_ROOT="supervisor-$TAG-$ARCH"
TMP_DIR="$(mktemp -d)"
STAGE_DIR="$TMP_DIR/$BUNDLE_ROOT"
IMAGE_TAR="$STAGE_DIR/image.tar"
BUNDLE_TAR="$BUILD_DIR/$BUNDLE_ROOT.bundle.tar.gz"
HELLO_STAGE="$TMP_DIR/hello-stage"
HELLO_BIN="$TMP_DIR/hello"

trap 'rm -rf "$TMP_DIR"' EXIT
mkdir -p "$STAGE_DIR/chart" "$STAGE_DIR/global/supervisor" "$STAGE_DIR/global/origin/hello/versions" "$STAGE_DIR/global/origin/hello/images" "$HELLO_STAGE/bin"

echo "==> ko build -> $IMAGE_TAR  ($IMAGE_REPO:$TAG, $PLATFORM)"
KO_DOCKER_REPO="$IMAGE_REPO" ko build \
  --push=false \
  --tarball="$IMAGE_TAR" \
  --bare \
  --tags="$TAG" \
  --platform="$PLATFORM" \
  ./cmd/supervisor

echo "==> build hello component -> $HELLO_GOOS/$HELLO_GOARCH"
(
  cd examples/supervisor/hello
  GOOS="$HELLO_GOOS" GOARCH="$HELLO_GOARCH" CGO_ENABLED=0 go build -o "$HELLO_BIN" .
)
cp "$HELLO_BIN" "$HELLO_STAGE/bin/hello"
cp examples/supervisor/hello/manifest.yml "$HELLO_STAGE/manifest.yml"
cp examples/supervisor/hello/greeting.txt.tmpl "$HELLO_STAGE/greeting.txt.tmpl"

HELLO_ARCHIVE="$STAGE_DIR/global/origin/hello/images/${HELLO_VERSION}_${HELLO_GOOS}_${HELLO_GOARCH}.tar.gz"
tar -C "$HELLO_STAGE" -czf "$HELLO_ARCHIVE" bin/hello manifest.yml greeting.txt.tmpl
printf '%s\n' "$HELLO_VERSION" > "$STAGE_DIR/global/origin/hello/versions/required.txt"

echo "==> stage chart, values, and global config"
cp -R "$DEPLOY_DIR/chart/." "$STAGE_DIR/chart/"
sed \
  -e "s|tag: dev|tag: $TAG|g" \
  -e "s|backoffice: supervisor.203.0.113.10.nip.io|backoffice: $BACKOFFICE_DOMAIN|g" \
  -e "s|public: hello.203.0.113.10.nip.io|public: $PUBLIC_DOMAIN|g" \
  "$DEPLOY_DIR/values.yaml" > "$STAGE_DIR/values.yaml"
cp "$DEPLOY_DIR/config.yml" "$STAGE_DIR/global/supervisor/config.yml"

cat > "$STAGE_DIR/manifest.json" <<EOF
{
  "app": "supervisor",
  "tag": "$TAG",
  "arch": "$ARCH",
  "image": "$IMAGE_REPO:$TAG",
  "platform": "$PLATFORM",
  "backoffice_domain": "$BACKOFFICE_DOMAIN",
  "public_domain": "$PUBLIC_DOMAIN",
  "hello_version": "$HELLO_VERSION",
  "created_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF

cat > "$STAGE_DIR/README.txt" <<EOF
Deploy with:
  ./deploy/supervisor/deploy.sh $BUNDLE_TAR

Backoffice: https://$BACKOFFICE_DOMAIN/
Hello app:  https://$PUBLIC_DOMAIN/greet
EOF

echo "==> write bundle -> $BUNDLE_TAR"
tar -C "$TMP_DIR" -czf "$BUNDLE_TAR" "$BUNDLE_ROOT"

echo "==> packaged $IMAGE_REPO:$TAG"
echo "==> bundle ready: $BUNDLE_TAR"
