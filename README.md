# OCP VCF Dashboard

A single-pane operational dashboard for OpenShift vSphere CI infrastructure. Provides a real-time web view of Prow jobs, vCenter capacity pools, leases, networks, and live vCenter inventory — all in one place.

## What It Does

The dashboard aggregates data from three sources into a unified web UI:

- **Kubernetes CRDs** (Pools, Leases, Networks) from [vsphere-capacity-manager](https://github.com/openshift-splat-team/vsphere-capacity-manager) — watched in real-time via informers
- **Prow CI Jobs** — fetched from the OpenShift Prow API, filtered to vSphere periodic jobs, with failure rates and sparkline history (ported from [vsphere-prow-summary](https://github.com/openshift-splat-team/vsphere-prow-summary))
- **vCenter Inventory** — clusters, datastores, and CI VMs collected via govmomi using the same credentials as [vsphere-capacity-manager-vcenter-ctrl](https://github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl)

Updates are pushed to the browser in real-time via Server-Sent Events (SSE) — no polling or page refresh needed.

## Dashboard Pages

| Page | Description |
|------|-------------|
| **Overview** | Summary cards: pool counts, active leases, failing Prow jobs, vCenter status; network usage-over-time charts (single/multi-tenant %, 1h–7d ranges) |
| **Pools** | Pool table with vCPU/memory utilization progress bars, scheduling status; pool detail has CPU/memory usage graphs |
| **Leases** | Active leases with phase, assigned pools/networks, job links |
| **Networks** | VLAN allocation, port groups, assignment status |
| **Prow Jobs** | vSphere periodic job status with sparklines, failure rates, version grouping |
| **vCenter** | Per-vCenter cluster stats, datastore usage, CI VM inventory with deep-links into vCenter |

## Prerequisites

- Access to an OpenShift cluster running `vsphere-capacity-manager` in the `vsphere-infra-helpers` namespace
- The `vsphere-cleanup-config` ConfigMap and `vcenter-*-credentials` Secrets already deployed (shared with vcenter-ctrl)
- Go 1.23+ for building
- `podman` for container builds
- `oc` CLI for deployment
- A `cluster-monitoring-view` role binding for Prometheus/Thanos queries (see `config/base/monitoring-clusterrolebinding.yaml`) — without it, the pool allocation series in the graphs will be empty

## Quick Start

### Run Locally

```bash
# Build
make build

# Run against your current kubeconfig
./bin/ocp-vcf-dashboard \
  --secret-namespace=vsphere-infra-helpers \
  --config-name=vsphere-cleanup-config \
  --v=2

# Open http://localhost:8080
```

### Deploy to OpenShift

```bash
# Build and push the container image
make image push

# Deploy via kustomize
make deploy

# The dashboard will be available at the Route created in vsphere-infra-helpers
oc get route ocp-vcf-dashboard -n vsphere-infra-helpers
```

### Remove

```bash
make undeploy
```

## Configuration

The dashboard reads the **same ConfigMap and Secrets** used by `vsphere-capacity-manager-vcenter-ctrl`. No additional configuration is needed.

### Command-Line Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--listen-address` | `:8080` | Address for the web UI |
| `--secret-namespace` | `vsphere-infra-helpers` | Namespace for ConfigMap and Secrets |
| `--config-name` | `vsphere-cleanup-config` | Name of the vCenter configuration ConfigMap |
| `--prow-poll-interval` | `5m` | How often to refresh Prow job data |
| `--vcenter-poll-interval` | `5m` | How often to poll vCenter inventory (`0` to disable) |
| `--console-url` | `https://console-openshift-console.apps.build02.vmc.ci.openshift.org` | OpenShift console base URL for namespace links |
| `--prometheus-url` | `https://thanos-querier.openshift-monitoring.svc:9091` | Prometheus/Thanos endpoint for pool graphs (`""` to disable) |
| `--prometheus-token-path` | (SA token) | Bearer token file for Prometheus queries |
| `--prometheus-skip-tls-verify` | `true` | Skip TLS certificate verification for Prometheus |
| `--kubeconfig` | (auto) | Path to kubeconfig; uses in-cluster config if empty |
| `--v` | `0` | klog verbosity level (`2` for periodic status logs) |

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│  OpenShift Route (TLS)                                   │
│    → oauth-proxy sidecar → dashboard :8080               │
│                                                          │
│  ┌─────────────────────────────────────────────────────┐ │
│  │  Go Binary                                          │ │
│  │                                                     │ │
│  │  K8s Informers ──┐                                  │ │
│  │  Prow Fetcher  ──┼──→ In-Memory Store ──→ SSE Push  │ │
│  │  vCenter Poller ─┘    (sync.RWMutex)     to browser │ │
│  │                                                     │ │
│  │  HTTP Server (Go templates + HTMX) + Graph API      │ │
│  └─────────────────────────────────────────────────────┘ │
│                                                          │
│  Reads: ConfigMap/vsphere-cleanup-config                 │
│         Secrets/vcenter-*-credentials                    │
│         CRDs: Leases, Pools, Networks                    │
│         External: prow.ci.openshift.org                  │
│                Thanos querier (Prometheus metrics)       │
└──────────────────────────────────────────────────────────┘
```

### Data Flow

- **CRDs**: Watched in real-time via `client-go` dynamic informers. Changes propagate to the browser within seconds.
- **Prow**: Fetched from `prow.ci.openshift.org/prowjobs.js` every `--prow-poll-interval`. Filtered to vSphere periodic jobs with OCP version >= 4.12.
- **vCenter**: Each configured vCenter is polled every `--vcenter-poll-interval` using govmomi. Collects cluster resources, datastore capacity, and CI-related VMs.
- **Graphs**: `/api/graph/pool/{name}` overlays Prometheus pool allocation with live vSphere cluster demand; `/api/graph/networks` serves per-pool single/multi-tenant network usage computed from the `pool_networks_*_by_type` metrics the vsphere-capacity-manager publishes to Prometheus (requires `--prometheus-url`, enabled by default). Rendered with uPlot on the Overview and pool detail pages.

### Authentication

- **Dashboard users**: Authenticated via OpenShift OAuth Proxy sidecar. Any authenticated OpenShift user can view.
- **vCenter access**: Uses credentials from the same Kubernetes Secrets deployed for vcenter-ctrl (`username`/`password` keys, CAPV identity pattern).
- **Kubernetes API**: Runs as ServiceAccount `ocp-vcf-dashboard` with read-only ClusterRole for CRDs, ConfigMaps, Secrets, and Namespaces.

## Development

```bash
make build    # fmt + vet + compile
make test     # run tests with coverage
make image    # build container with podman
make run      # build + run locally
```

See [AGENTS.md](AGENTS.md) for detailed code style guidelines and conventions.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
