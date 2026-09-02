# SG-P0-LOCAL execution record

## Scope and authority

- Baseline: `main@1c9b7d9eeb5eac6edc33200e613d9e7e5baefa3a`
- Module path: `github.com/zhangzhe-ctrl/ani-session-gateway`
- Allow: this repository's SG-0 through SG-4 implementation, tests, static manifests, and execution evidence.
- Deny: Git staging/commit/tag/push/remote changes; ANI changes; SSH; deployment; cluster mutation; ANI-GW-1; LIVE-1.
- Existing Git remote: `origin` was present and was not changed by this Work Package.
- Rollback: discard only the uncommitted files listed by this record; no external state was changed.

## SG-0 — LOCAL_VERIFIED

Implemented the root and API Go modules, the single `CreateSession` protobuf interface, generated Go client/server, OpenAPI contract projection, fail-fast configuration, HTTP health/readiness/metrics routes, gRPC health and explicit pre-SG-1 `Unimplemented`, Docker build skeleton, and repeatable Make targets.

Commands executed:

```text
make tools
XDG_CACHE_HOME=/tmp/ani-sg-xdg PATH="$PWD/.bin:$PATH" buf lint
XDG_CACHE_HOME=/tmp/ani-sg-xdg PATH="$PWD/.bin:$PATH" buf generate
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go mod tidy
(cd api && GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go mod tidy)
PATH="$PWD/.bin:$PATH" XDG_CACHE_HOME=/tmp/ani-sg-xdg GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache make lint contract check-generated test race vet build diff-check
```

Result: passed. Root and API modules build/test/vet independently; protobuf regeneration is stable; config rejects missing public URL, origins and ticket key, rejects non-32-byte raw keys, and rejects `ws://` for HTTPS origins; HTTP and gRPC probes pass.

Not verified:

- `buf breaking` against a historical API tag: no `api/v0.1.0` tag exists and this Goal forbids creating one. Establish the baseline after the user's first manual API publication.

Deviation/risk:

- Buf `PACKAGE_DIRECTORY_MATCH` is explicitly excepted because the design fixes source location at `api/proto/session/v1` while fixing the protobuf package at `ani.session.v1`.

## SG-0A — TECH_BASELINE_CORRECTED

The user paused the Goal after discovering that the original selected technology stack was omitted from v1.1. Design v1.2 now freezes `net/http + go-chi/chi/v5`, `coder/websocket`, `grpc-go`, `client-go/tools/remotecommand`, `kubevirt.io/client-go`, Redis, Prometheus Go Client, OpenTelemetry Go, and environment-variable fail-fast configuration.

Corrections completed before continuing SG-1:

- replaced the SG-0 `http.NewServeMux` composition with one chi Router while retaining `net/http.Server`, both listeners and graceful shutdown;
- added explicit OpenTelemetry initialization/shutdown plus fixed-name HTTP, gRPC, SessionManager and SessionStore spans without sensitive attributes/events;
- made Redis the default production runtime and removed `STORE_MODE=auto` plus Redis-to-Memory fallback;
- retained MemoryStore only for shared contract tests and explicit local development, with readiness and metrics reporting local/degraded;
- restored the repository's existing `.gitignore` rules and added only local tool/binary ignores.

Commands executed:

```text
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go mod tidy
rg -n "http\\.NewServeMux|gorilla/websocket|STORE_MODE.*auto|StoreMode == \\"auto\\"|StoreMode: \\"auto\\"" --glob '*.go' --glob '!**/*_test.go' .
PATH="$PWD/.bin:$PATH" XDG_CACHE_HOME=/tmp/ani-sg-xdg GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache make check
```

Result: passed. Router GET/404/405/recovery behavior, Redis default/fail-fast/no-fallback, explicit Memory local/degraded, tracing init/idempotent shutdown and sensitive-field exclusion tests passed. The production-code guard search returned no matches. `make check` passed protobuf lint/drift, OpenAPI contract, root/API tests, race, vet, binary build and `git diff --check`.

Not verified:

- OTLP export to a real collector: `OTEL_EXPORTER_OTLP_ENDPOINT` was not configured. Initialization and endpoint validation are locally verified; collector delivery remains `not_verified`.

No implementation code was changed by the design-closure turn. Resume contract: `docs/execution/resume-SG-P0-LOCAL.md`.

## SG-1 — LOCAL_VERIFIED

Completed and audited the retained SessionManager, MemoryStore, RedisStore, selection code and shared contract tests. The manager issues 32-byte random tickets, stores only SHA-256 digest plus AES-256-GCM ciphertext, deterministically fingerprints all request semantics except request ID, replays only an issued/unexpired result, and maps the generated gRPC `CreateSession` interface to the same lifecycle.

Redis `CreateOrGet` and `ClaimAndReserve` use atomic Lua scripts. Claim performs a full fixed-length digest scan, state transition, expired-lease cleanup, global/subject capacity checks, ciphertext removal and lease write atomically. Idempotency and subject Redis key material is hashed. Claim extends the tombstone through lease expiry plus `IDEMPOTENCY_TTL`, preventing deletion of active sessions claimed near ticket expiry. Redis startup and runtime failures return unavailable and never select MemoryStore.

Commands executed:

```text
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go test ./internal/session ./internal/store/... ./internal/transport/grpc
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go mod tidy
rg -n "STORE_MODE.*auto|StoreMode == \\"auto\\"|StoreMode: \\"auto\\"" --glob '*.go' --glob '!**/*_test.go' .
PATH="$PWD/.bin:$PATH" XDG_CACHE_HOME=/tmp/ani-sg-xdg GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache make check
docker run --rm -d --name ani-sg-redis-test -p 127.0.0.1::6379 redis:7.4-alpine
REDIS_TEST_ADDR=127.0.0.1:32768 GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go test -race ./internal/store/redis -run TestContractAgainstRealRedis -count=1 -v
docker stop ani-sg-redis-test
```

Result: passed. Both adapters passed the same contract suite, including 100 concurrent claims with exactly one success, conflict/precondition/replay rules, capacity limits, lease expiry recovery, idempotent close and tombstone behavior. Redis dump inspection found no plaintext ticket; a simulated runtime disconnect returned unavailable without fallback. Generated gRPC client/server issue and replay round trip passed. A real Redis 7.4 container passed the contract suite under the race detector, then was stopped and removed.

Not verified:

- Redis Cluster/HA and an actual Session Gateway process crash are live/environment checks outside this local Work Package. Atomic single-Redis multi-process semantics and lease-expiry recovery are covered locally.

## SG-2 — LOCAL_VERIFIED

Implemented the terminal WebSocket endpoint on the same chi Router using `coder/websocket`. Ticket claim occurs only after exact origin and known-subprotocol prechecks; the claimed session mode then fixes the accepted subprotocol. Terminal text frames cover stdin, stdout, stderr, resize, exit and sanitized error messages. Bounded inbound/outbound queues, write deadlines, maximum message size, idle timeout, lease maximum duration, ping/pong, cancellation and close handling fail closed without logging tickets, credentials or stream payloads.

Implemented the Kubernetes Pod exec adapter behind the small `ExecRuntime`/`ExecStream` port. The resolver derives `ani-tenant-<tenant_id>`, requests both tenant and instance labels, rechecks both labels on every returned Pod, selects the newest non-terminating Running/Ready Pod, and requires an unambiguous container. The adapter uses the typed CoreV1 client and `client-go/tools/remotecommand` SPDY executor, keeps stdout/stderr distinct when non-TTY, propagates stdin and resize, maps exit status, and cancels the stream on close.

Commands executed:

```text
gofmt -w cmd/session-gateway/main.go internal/runtime/kubernetes internal/transport/websocket
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go test -race ./internal/runtime/... ./internal/transport/websocket -count=1
rg -n --glob '*.go' '(slog\.(Debug|Info|Warn|Error)|log\.(Print|Printf|Println)).*(ticket|credential|payload|stdin|stdout|stderr|RawQuery|RequestURI)' .
rg -n --glob '*.go' 'gorilla/websocket|http\.NewServeMux|STORE_MODE.*auto|fallback.*memory' .
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache make check
```

Result: passed. Race tests cover terminal frames and exit, ticket replay, exact-origin and subprotocol rejection before claim, invalid binary input, inbound backpressure, idle timeout, lease maximum duration, message-size limit, sanitized runtime errors, Pod readiness/container ambiguity/double-label enforcement, request URL construction, stdin/stdout/stderr/resize, exit status and cancellation. Full generation, root/API test, race, vet, build and diff gates passed. The sensitive-log search returned no matches; the only baseline-guard match was the intentional test that verifies `STORE_MODE=auto` is rejected.

Not verified:

- A real Kubernetes API/SPDY Pod exec smoke test is `not_verified`: this Work Package has no cluster authority and did not contact or mutate a cluster. The REST resolution/request and stream protocol are covered by local adapter tests.

## SG-3 — LOCAL_VERIFIED

Pinned `kubevirt.io/client-go` at v1.8.2 and retained its Kubernetes alignment at `k8s.io/{api,apimachinery,client-go}` v0.34.3. Because the published client module declares the nonexistent `k8s.io/kube-openapi v0.31.0`, the root module carries the exact replacement shipped by KubeVirt v1.8.2 (`v0.0.0-20250710124328-f3f2b991d03b`) rather than changing KubeVirt versions or using latest.

Implemented the `VMConsoleRuntime` adapter with a defensive VMI GET before each privileged stream. It derives the tenant namespace and verifies returned namespace, name, tenant label, instance label, non-deleting state and `Running` phase. The production adapter uses the v1.8.2 `AsyncSubresourceHelper` paths `virtualmachineinstances/console` and `virtualmachineinstances/vnc`; provider-side negotiation is `plain.kubevirt.io`. Its stream is driven through the upstream `Stream(In/Out)` lifecycle and exposed only as the local `ByteStream` port, keeping KubeVirt and exec interfaces separate.

Serial browser traffic uses the terminal JSON subprotocol with stdin/stdout only; VNC uses transparent binary WebSocket frames without JSON or base64. Both paths enforce ticket lifecycle, mode-specific subprotocols, message limits, idle and lease deadlines, cancellation, sanitized close behavior, and raw byte backpressure.

Commands executed:

```text
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go get kubevirt.io/client-go@v1.8.2
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go mod tidy
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go test -race ./internal/runtime/kubevirt ./internal/transport/websocket -count=1
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go list -m kubevirt.io/client-go k8s.io/client-go k8s.io/api k8s.io/apimachinery
rg -n --glob '*.go' --glob '!**/*_test.go' '(slog\.(Debug|Info|Warn|Error)|log\.(Print|Printf|Println)).*(ticket|credential|payload|stdin|stdout|stderr|RawQuery|RequestURI)' .
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache make check
```

Result: passed. Version audit returned KubeVirt v1.8.2 and all three Kubernetes modules v0.34.3. Unit/race tests cover byte preservation, serial newline/output, VNC RFB-like binary bytes in both directions, VMI identity/tenant/Running checks, not-found/unavailable mapping, connection timeout, context cancellation, late-connection cleanup, abnormal close, ticket replay, invalid VNC text frames and session maximum duration. A local provider-path test verifies the real REST GET plus WebSocket `console`/`vnc` paths, `plain.kubevirt.io`, and `preserveSession=false`. Full repository gates and the sensitive-log search passed.

Not verified:

- Real KubeVirt serial login/output is `not_verified`.
- Real VNC RFB handshake, rendering, keyboard and mouse input are `not_verified`.
- No cluster was contacted or mutated; these smoke tests require separately authorized live fixtures and are not represented as passed.

## SG-4 — MANIFEST_VERIFIED

Created a Kustomize base containing the dedicated ServiceAccount, exact minimal ClusterRole/Binding, fail-fast ConfigMap, Deployment, ClusterIP-only gRPC Service, WebSocket NodePort Service, and default-deny NetworkPolicy. The Deployment fixes `STORE_MODE=redis`, references Redis and ticket key values from one external Secret, mounts the decoded `ticket-encryption-key` read-only at the configured path, and relies on the process's fail-fast exact-32-byte check. It includes health/readiness probes, resources, a 30-second termination grace period, non-root/seccomp/read-only-root-filesystem security controls and no hostPort.

The NetworkPolicy exposes container port 8080 for browser/probe traffic, while 9090 requires both the `ani-system` namespace label and `ani-gateway` Pod label. Egress explicitly covers DNS, Redis, a cluster-specific Kubernetes API `/32`, and the KubeVirt `virt-api` selector/port. The handoff documents required environment substitutions, NodePort collision inspection, Secret creation, server dry-run and positive/negative `kubectl auth can-i` checks without executing them.

Added a strict typed manifest test and a dedicated `make manifests` gate. The test rejects unknown Kubernetes fields, verifies all eight objects and Kustomize references, exact RBAC rules/no wildcards, Service exposure, ingress double selector, egress selectors/ports, security context, probes/resources, Redis Secret reference and exact ticket-key mount. The key validator accepts only a regular file of exactly 32 raw bytes; its positive and negative paths run in the gate. The cross-cutting final audit also completed the v1.2 minimum Prometheus metric set and structured lifecycle logging without sensitive labels or payloads, and aligned the Docker build image with root `go 1.25`.

Commands executed:

```text
sh -n scripts/check-ticket-key.sh scripts/check-manifests.sh
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache make manifests
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go test -race ./internal/observability ./internal/session ./internal/runtime/kubevirt ./internal/transport/websocket ./internal/deployment -count=1
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache make check
rg -n --glob '*.go' --glob '!**/*_test.go' '(slog\.(Debug|Info|Warn|Error)|log\.(Print|Printf|Println)).*(ticket|credential|payload|stdin|stdout|stderr|RawQuery|RequestURI|ciphertext)' .
rg -n --glob '*.go' --glob '!**/*_test.go' 'http\.NewServeMux|STORE_MODE.*auto|StoreMode == "auto"|StoreMode: "auto"' .
GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go build ./...
(cd api && GOMODCACHE=/tmp/ani-sg-gomod GOCACHE=/tmp/ani-sg-gocache go build ./...)
git diff --check
docker build --tag ani-session-gateway:sg-p0-local .
```

Result: passed. YAML documents strictly decode into Kubernetes API types; semantic deployment/RBAC/network gates and both ticket-key length paths passed. Full generation, root/API tests, race, vet, build and diff gates passed. Root and API modules also built independently. The distroless container image built locally as `ani-session-gateway:sg-p0-local` (final manifest list digest `sha256:5fbf5ada66723bee0b3ea69293f6adb06b0791ed93d99f59d239bd437eb34de3`); `.dockerignore` reduced the build context from 175 MB to 32 KB. Sensitive-log and obsolete-stack scans returned no production matches.

Not verified:

- `kubectl kustomize`, client/server dry-run and API schema discovery are `not_verified`: `kubectl` and `kustomize` are unavailable locally and no cluster access is authorized.
- Live `kubectl auth can-i` allow/deny results, NetworkPolicy enforcement, external inability to reach 9090, browser reachability of NodePort 30081, NodePort collision, Pod restart behavior and Redis persistence are `not_verified`.
- The external Secret's real ticket-key bytes and Redis URL are `not_verified`; the manifest mount is statically verified and the process rejects any mounted key not exactly 32 raw bytes.

## Final local state

SG-0 through SG-3 are `LOCAL_VERIFIED`; SG-4 is `MANIFEST_VERIFIED`. This record does not assert `DEPLOYED`, `LIVE`, `PRODUCTION_READY`, HA, mTLS, real Pod exec, real KubeVirt console/VNC, or end-to-end ANI/Console behavior.

## Module-path migration — LOCAL_VERIFIED

The user fixed the repository module path as `github.com/zhangzhe-ctrl/ani-session-gateway`, matching the existing `origin`. The root module, API submodule, source imports, protobuf `go_package`, generated code, README and execution state were migrated from the previous provisional module path. No remote, commit, push or tag operation was performed.

Commands executed:

```text
GOMODCACHE=/tmp/ani-sg-gomod go mod tidy
(cd api && GOMODCACHE=/tmp/ani-sg-gomod go mod tidy)
PATH="$PWD/.bin:$PATH" XDG_CACHE_HOME=/tmp/ani-sg-xdg GOMODCACHE=/tmp/ani-sg-gomod make generate
PATH="$PWD/.bin:$PATH" TMPDIR=/home/chabking/Documents/Codex/2026-09-02/zhe/.ani-sg-tmp XDG_CACHE_HOME=/tmp/ani-sg-xdg GOMODCACHE=/tmp/ani-sg-gomod make check
go list -m
(cd api && go list -m)
rg -n --hidden --glob '!.git/**' 'github[.]com/kubercloud/ani-session-gateway' .
git remote get-url origin
git diff --check
docker build --tag ani-session-gateway:module-path-ready .
```

Result: passed. The root module reports `github.com/zhangzhe-ctrl/ani-session-gateway`; the API module reports `github.com/zhangzhe-ctrl/ani-session-gateway/api`; `origin` reports `https://github.com/zhangzhe-ctrl/ani-session-gateway.git`; the old module-path search returned no matches. Buf lint/generation drift, OpenAPI and manifest contracts, root/API tests, race, vet, binary build and diff checks passed. The local container build produced manifest list digest `sha256:494a454bf1206fc37b60570a1586be41bd203b5f76194de10e806fbaf210bfff`.

The first two `make check` attempts were environment-only failures: the sandbox initially prohibited loopback listeners used by `httptest`/miniredis, then `/tmp` exhausted its quota during the race build. Re-running with loopback permission and a capacity-sufficient `TMPDIR` passed without source changes.

Still human-owned and not performed: Git staging/commit/push and creation/push of Git tag `api/v0.1.0`. `buf breaking` remains `not_verified` until that first API baseline tag exists.
