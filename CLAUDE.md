# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Test Commands

```bash
make build              # go fmt + go vet + compile to bin/ocp-vcf-dashboard
make test               # go test ./... -coverprofile cover.out
make run                # build + run locally with default flags
make image              # podman build (not docker)
make deploy             # oc apply -k config/default
make undeploy           # oc delete -k config/default
```

Run a single test:
```bash
go test ./pkg/store/ -run TestSetPool -v -count=1
```

Dependencies are vendored but `vendor/` is gitignored. After changing `go.mod`:
```bash
go mod tidy && go mod vendor
```

Three `replace` directives exist in go.mod — do not remove them.

## Architecture

Real-time operational dashboard for OpenShift vSphere CI infrastructure. Three data sources feed a central in-memory store, which pushes updates to browsers via SSE:

- **K8s Informers** (`pkg/k8s/watcher.go`): dynamic informers watch Pool/Lease/Network CRDs in real-time
- **Prow Fetcher** (`pkg/prow/`): polls `prow.ci.openshift.org` for vSphere periodic jobs on a configurable interval
- **vCenter Collector** (`pkg/vcenter/collector.go`): polls each configured vCenter via govmomi for cluster stats, datastore capacity, and CI VMs

All three write to the **Store** (`pkg/store/store.go`), a thread-safe (`sync.RWMutex`) in-memory data store that notifies SSE subscribers on changes. The store uses `DeepCopy()` on all CRD objects at write and read boundaries.

The **HTTP server** (`pkg/server/`) renders Go `html/template` pages with HTMX for live updates. Each page has a full-page route, a `/fragments/` route for HTMX partial updates, and an `/api/sse/` endpoint for push. Templates are embedded via `//go:embed views/*.html`.

Entry point is `cmd/dashboard/main.go` which wires all components together. Config is read from the same ConfigMap/Secrets used by vsphere-capacity-manager-vcenter-ctrl (no separate config needed).

## Code Conventions

See [AGENTS.md](AGENTS.md) for the complete style guide, covering:

- **Imports**: three groups (stdlib, third-party, local) separated by blank lines
- **Import aliases**: `corev1`, `metav1`, `vcmv1`, `ctrlclient`, `k8swatcher`
- **Naming**: single-letter pointer receivers, `New*()` constructors, `xxxLocked` for lock-held helpers, `handleXxx` for HTTP handlers
- **Errors**: always `fmt.Errorf("gerund phrase: %w", err)`, never `errors.New`
- **Logging**: `klog` exclusively, `V(2)` for periodic/routine, `Fatalf` only in `main()`
- **Concurrency**: `RLock` for getters, explicit `Unlock()` before `notify()` in setters (no defer)
- **JSON tags**: `snake_case` with `omitempty` only for optional fields
- **Container**: uses podman, multi-stage with distroless, runs as non-root `65532:65532`
