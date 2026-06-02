# Supervisor Deployment Summary

## Goal

The immediate supervisor goal is now concrete and deployed: build `cmd/supervisor` with `ko`, wrap it in a self-contained bundle with a frozen Helm chart, deploy it to `hertzner1`, and run a minimal hello-world component that is updated from files under the persistent global folder.

The deployed supervisor separates three runtime surfaces:

| Surface | Current value | Purpose |
| --- | --- | --- |
| Global folder | host `/kubestorage/globals/supervisor`, container `/global` | Persistent supervisor state, component update origin, logs, and operator-edited state |
| Backoffice/control | `https://supervisor.62.238.27.201.nip.io/backoffice/...` | Supervisor health, state, summary, metrics, component info, logs |
| Public app | `https://hello.62.238.27.201.nip.io/...` | User-facing hello component running as a supervisor child |

Both domains are currently restricted to the operator IP `46.121.9.131/32`.

## Current Deployment

The live release is a Helm release named `supervisor` in namespace `supervisor`.

Observed working endpoints:

- `https://hello.62.238.27.201.nip.io/greet`
  - returns `Hello from supervisor on hertzner1`
- `https://hello.62.238.27.201.nip.io/healthz`
  - returns `ok`
- `https://supervisor.62.238.27.201.nip.io/backoffice/healthz`
  - returns `ok`
- `https://supervisor.62.238.27.201.nip.io/backoffice/summary`
  - reports `hello` as `pass`, `current: v2`, `port: 18090`

Current Kubernetes shape:

| Resource | Shape |
| --- | --- |
| Deployment | `supervisor`, `replicas: 1`, `strategy.type: Recreate` |
| Image | `localhost/supervisor:<tag>` loaded directly into k3s containerd |
| Service | `supervisor-backoffice` maps service port 80 to pod port `9090` |
| Service | `supervisor-public` maps service port 80 to pod port `18090` |
| Ingress | `supervisor-backoffice` for the control domain |
| Ingress | `supervisor-public` for the public app domain |
| Middleware | `supervisor-ip-allowlist`, `sourceRange: ["46.121.9.131/32"]` |

`Recreate` is required. The supervisor holds `/global/supervisor/state/supervisor.lock`; a rolling update starts a second pod before the first exits, and the new pod crashes on the shared lock.

## hertzner1 Assumptions

The deployment follows the same single-node k3s model used by `jsonmcpserver` and `mocks`:

- Single-node k3s on `hertzner1`.
- Traefik ingress.
- cert-manager with `letsencrypt-prod`.
- No external image registry.
- Images are built locally with `ko --push=false --tarball=image.tar`.
- Bundles are uploaded over SSH.
- The image tarball is imported with `sudo k3s ctr images import`.
- Workloads refer to images as `localhost/<app>:<tag>` with `imagePullPolicy: IfNotPresent`.
- Runtime user/group is `65532:65532`.
- Persistent app state is host-visible under `/kubestorage/globals/<app>`.

One cluster-level setting matters for IP allowlisting:

```sh
kubectl --kubeconfig ~/.kube/hertzner1.yaml \
  -n kube-system patch svc traefik \
  -p '{"spec":{"externalTrafficPolicy":"Local"}}'
```

Without `externalTrafficPolicy: Local`, k3s ServiceLB SNATs traffic before it reaches Traefik, and Traefik's `ipAllowList` sees a cluster/internal source instead of the real client IP.

## Bundle Contract

`deploy/supervisor/package.sh` writes:

```text
build/supervisor-<tag>-arm64.bundle.tar.gz
└── supervisor-<tag>-arm64/
    ├── image.tar
    ├── chart/
    ├── values.yaml
    ├── manifest.json
    ├── README.txt
    └── global/
        ├── supervisor/config.yml
        └── origin/hello/
            ├── versions/required.txt
            └── images/v1_linux_arm64.tar.gz
```

The bundle is self-contained for this demo:

- `image.tar` contains the supervisor image.
- `chart/` is the frozen Helm chart used for deployment.
- `values.yaml` contains the image tag, domains, ports, hostPath, and IP allowlist.
- `global/supervisor/config.yml` seeds the supervisor config.
- `global/origin/hello/...` seeds a local `file:///global/origin` update origin for the hello component.

`deploy/supervisor/deploy.sh` consumes only the bundle. It uploads it to `hertzner1`, imports the image into k3s containerd, copies `global/.` into `/kubestorage/globals/supervisor/`, and runs `helm upgrade --install`.

## Commands

Build the bundle:

```sh
make package-supervisor
```

Deploy the newest bundle:

```sh
make deploy-supervisor
```

Build and deploy:

```sh
make ship-supervisor
```

Direct script form:

```sh
./deploy/supervisor/package.sh
./deploy/supervisor/deploy.sh build/supervisor-<tag>-arm64.bundle.tar.gz
```

Useful overrides:

```sh
BACKOFFICE_DOMAIN=supervisor.example.com \
PUBLIC_DOMAIN=hello.example.com \
HELLO_VERSION=v2 \
IMAGE_TAG=manual-v2 \
  ./deploy/supervisor/package.sh
```

To update the IP allowlist, edit `deploy/supervisor/values.yaml`:

```yaml
access:
  allowedCIDRs:
    - 46.121.9.131/32
```

Then repackage and redeploy.

## Supervisor Config

The seeded config lives at:

```text
/global/supervisor/config.yml
```

The current demo config:

```yaml
state_dir: /global/supervisor/state

supervisor:
  bind_address: 0.0.0.0:9090
  public_port: 18090
  metric_labels:
    source: hertzner1-supervisor-demo

vars:
  GREETING: "Hello from supervisor on hertzner1"

remote:
  base_url: file:///global/origin
  target: required.txt
  polling_interval: 10s
  signature_public_key_path: ""

components:
  - name: hello
    description: "Minimal hello world app updated from /global/origin"
    port: 18090
    command: "./bin/hello"
```

The hello component is a normal supervisor-managed child:

- Supervisor polls `/global/origin/hello/versions/required.txt`.
- It extracts `/global/origin/hello/images/v2_linux_arm64.tar.gz`.
- It best-effort validates `greeting.txt.tmpl` during prepare.
- It injects `GREETING` and launch facts into the child environment.
- It launches `./bin/hello` from the extracted version directory.
- Kubernetes exposes the child port through `supervisor-public`.

## Storage Layout

Current host-visible files:

```text
/kubestorage/globals/supervisor/
  supervisor/
    config.yml
    state/
      supervisor.lock
      hello/
        current.txt
        stable.txt
        versions/v2/
          bin/hello
          manifest.yml
          greeting.txt.tmpl
      logs/hello/v2/
        stdout.log
        stderr.log
  origin/
    hello/
      versions/required.txt
      images/v1_linux_arm64.tar.gz
```

This gives the demo its intended behavior: supervisor image upgrades, pod restarts, and supervisor restarts preserve component state and update artifacts.

## Supervisor Runtime Contract

Supervisor now best-effort validates component template files listed in `manifest.yml` with the shared go-run config engine (`vars:`, `{{ }}` / `[[ ]]`, `default`, `required`, `env`, `add`, etc.). It does not render application config files as part of normal launch.

The version manifest shape is:

```yaml
validate_templates:
  - config.yml

default_vars:
  GREETING: "Hello"
```

The listed `config.yml` corresponds to `config.yml.tmpl` in the artifact. Prepare validates in memory only. The application remains responsible for loading and decoding its own configuration.

At validation time and child launch time, supervisor exposes the same launch facts:

- `OP_VERSION`, `OP_VERSION_DIR`, `OP_STATE_DIR`, `OP_MONITOR_PORT`, `OP_KILL_SOCK`, `OP_LOG_DIR`
- `VERSION`, `REQUIRED_VERSION`, `VERSION_DIR`, `BUILD_DIR`, `BUILDDIR`, `STATE_DIR`, `MONITOR_PORT`, `KILL_SOCK`, `LOG_DIR`

`BUILD_DIR` / `BUILDDIR` point at the extracted version directory, so a locally built artifact can be tested under supervisor using the same directory assumptions as the build pipeline.

## Access Control

Both public routes use the same Traefik middleware:

```yaml
apiVersion: traefik.io/v1alpha1
kind: Middleware
metadata:
  name: supervisor-ip-allowlist
  namespace: supervisor
spec:
  ipAllowList:
    sourceRange:
      - 46.121.9.131/32
```

Both ingresses reference it with:

```yaml
traefik.ingress.kubernetes.io/router.middlewares: supervisor-supervisor-ip-allowlist@kubernetescrd
```

Operational note: if the operator IP changes, both domains return `403` until `access.allowedCIDRs` is updated and the chart is redeployed.

## Runtime Capabilities Exercised

This deployment exercises the supervisor features that matter for the next project step:

- `ko` build of the supervisor image.
- Bundle-based deployment with a frozen Helm chart.
- Persistent `/global` storage.
- `file://` update origin under `/global/origin`.
- Component extraction into `/global/supervisor/state`.
- Best-effort manifest template validation during prepare.
- Environment injection before launch.
- Child process launch in the supervisor pod network namespace.
- Separate Kubernetes services/ingresses for backoffice and public app traffic.
- Traefik IP allowlist at the ingress layer.

## Remaining Work

The demo is deployed and working, but several items remain design work:

- Replace `nip.io` demo domains with real DNS records when ready.
- Decide whether `public_port` remains a single app port or becomes a structured list of public component routes.
- Decide whether public traffic should be direct-to-child, as in this demo, or proxied by supervisor.
- Move from unsigned `file://` demo artifacts to signed HTTPS update artifacts for production.
- Decide whether customer Kubernetes should use PVC-backed `/global` while hertzner1 continues to use hostPath.
- Make Traefik `externalTrafficPolicy: Local` part of documented cluster bootstrap, since it is required for source-IP allowlists.
