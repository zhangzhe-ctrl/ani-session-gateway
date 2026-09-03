# Execution Status

```yaml
project: ANI Realtime Session Gateway P0
design_revision: v1.2
design_status: CLOSED
implementation_status: DEPLOYED_PARTIAL_PRODUCT_E2E_VERIFIED
active_work_package: SG-LIVE-DEPLOY-20260903
goal_run_status: DEPLOYED_PARTIAL_E2E_VERIFIED
resume_checkpoint: NONE
module_path: github.com/zhangzhe-ctrl/ani-session-gateway
remote: EXISTING_ORIGIN_UNCHANGED
git_authority: LOCAL_WORKTREE_ONLY
deployment_authority: ONE_TIME_ANI_SYSTEM_CONSUMED
ani_baseline: 963bc88836c54a1b09cf100b37eb2f2cb2a5a4be
last_updated: 2026-09-03
```

## Checkpoint evidence

| Checkpoint | Status | Evidence | Not verified / risk |
|---|---|---|---|
| SG-0 | LOCAL_VERIFIED_PRE_V1.2 | `buf lint`; generation drift; OpenAPI contract; root/API `go test` and `go vet`; race; binary/container build; config and probe tests; `git diff --check` passed | Router choice was superseded and corrected by SG-0A; historical `buf breaking` baseline absent |
| SG-0A | TECH_BASELINE_CORRECTED | Single chi Router; Redis default/fail-fast; explicit memory local/degraded; OpenTelemetry HTTP/gRPC/SessionManager/Store tracing; `go mod tidy`, full `make check`, race, vet, generation and guard search passed | OTLP collector export smoke not_verified; no collector configured |
| SG-1 | LOCAL_VERIFIED | SessionManager + gRPC; AES-GCM/digest/idempotency; Memory/Redis shared contract; 100 concurrent claim; capacity/lease/tombstone; no plaintext; Redis fail-fast/interruption; full `make check`; real Redis 7.4 race integration passed | Redis Cluster/HA and process-crash live behavior remain outside local scope |
| SG-2 | LOCAL_VERIFIED | coder/websocket terminal protocol; exact origin/subprotocol gate; bounded backpressure; idle/max-duration/message-size close; typed CoreV1 Pod resolver with namespace plus double-label verification; SPDY Pod exec; unit/race/full `make check` passed | Real Kubernetes API/SPDY Pod exec smoke not_verified; no cluster was contacted |
| SG-3 | LOCAL_VERIFIED | KubeVirt client-go v1.8.2 with Kubernetes v0.34.3; VMI Running/namespace/name/double-label defense; provider `console`/`vnc` plus `plain.kubevirt.io`; Serial JSON and raw binary VNC bridges; cancellation/timeout/error/race/full `make check` passed | Real KubeVirt serial login and VNC RFB/render/input smoke are not_verified; no cluster was contacted |
| SG-4 | MANIFEST_VERIFIED | Deployment; ClusterIP gRPC + NodePort WebSocket Services; minimal ClusterRole/Binding; external Secret references and exact read-only ticket-key mount; NetworkPolicy ingress/egress; strict typed manifest test; 32-byte key validator; full `make check` and local container build passed | `kubectl`/`kustomize` binaries and cluster unavailable; server dry-run, `auth can-i`, NetworkPolicy behavior, NodePort collision/access, real Secret bytes and restart persistence are not_verified |
| SG-LIVE-DEPLOY-20260903 | DEPLOYED_PARTIAL_E2E_VERIFIED | `ani-system` 中 Session Gateway 与 ANI Gateway 均 1/1 Ready；私库镜像、WS NodePort 30082、HTTP probes、Gateway 到 gRPC、Redis、RBAC allow/deny、Pod exec、VM serial JSON/stdout 与 VNC 101/binary RFB 握手已验证 | guest serial 登录/命令语义、VNC render/input、ticket 重放/过期/跨租户、NetworkPolicy 负向隔离与 restart persistence 仍未验证 |

Full commands and evidence: `docs/execution/records/SG-P0-LOCAL.md`.

## Repository publication readiness

- GitHub repository and `origin`: `https://github.com/zhangzhe-ctrl/ani-session-gateway.git` — VERIFIED, unchanged by the implementation Goal.
- Root module: `github.com/zhangzhe-ctrl/ani-session-gateway` — BOUND.
- API module: `github.com/zhangzhe-ctrl/ani-session-gateway/api` — BOUND.
- Source imports, protobuf `go_package`, generated code and execution evidence use the bound module path — LOCAL_VERIFIED; old path has zero worktree references.
- Full repository gates after the module-path migration — LOCAL_VERIFIED; `make check` and local container build passed.

## Human checkpoints

- API submodule 首次发布：用户手工 commit/push，并在包含完整 API 文件的提交上创建 Git tag `api/v0.1.0`；ANI 依赖时使用 module version `v0.1.0`。
- ANI-GW-1：用户提供已发布的 `<API_VERSION>` 与 ANI 精确 baseline。
- LIVE-1：用户单独授权 SSH、测试 namespace、fixtures、rollout、删除范围和回滚。
- 当前部署：用户已一次性授权部署到 `ani-system`，该授权已消费；继续执行 fixture 创建、故障注入、删除或完整 LIVE-1 仍需单独授权。
