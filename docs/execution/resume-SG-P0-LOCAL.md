# SG-P0-LOCAL Resume Contract (Historical)

> 状态：COMPLETED_NO_RESUME_REQUIRED  
> 当前动作：不要恢复旧 Goal；以 `docs/execution/status.md` 和 `docs/execution/records/SG-P0-LOCAL.md` 的最终状态为准  
> 本文件保留用于解释技术基线纠偏过程，不再是活动恢复入口

## Authority

本合同是现有 `SG-P0-LOCAL` Goal 的补充执行合同。权威设计已从 v1.1 修订为 `docs/design/realtime-session-gateway-p0-v1.2.md`；v1.2 补录用户最初选定的技术栈，并覆盖 v1.1 中与之冲突的运行策略。

恢复后先完成 `SG-0A-TECH-BASELINE`，通过后从工作树真实进度继续 SG-1～SG-4。不得重复生成仓库或丢弃已完成的 SG-0、部分 SG-1 成果。

## Frozen technology baseline

| 范围 | 强制选择 |
|---|---|
| HTTP Router | `net/http` + `github.com/go-chi/chi/v5` |
| WebSocket | `github.com/coder/websocket` |
| 内部控制接口 | `google.golang.org/grpc` |
| Pod Exec | `k8s.io/client-go/tools/remotecommand` |
| KubeVirt | `kubevirt.io/client-go` |
| Session Store | Redis；MemoryStore 仅测试/显式本地开发 |
| Metrics | `github.com/prometheus/client_golang` |
| Trace | OpenTelemetry Go |
| 配置 | 环境变量 + 启动时强校验 |

不得自行替换为 `http.ServeMux`、gorilla/websocket、Gin/Echo/Fiber/Kratos、自研 Router/WebSocket/DI/lifecycle framework，或 Redis 失败自动 MemoryStore 降级。

## Resume preflight

1. 完整读取 `AGENTS.md`、`CLAUDE.md`、v1.2 设计、status、本合同和 `SG-P0-LOCAL.md`。
2. 检查完整 Git 状态与 diff；保留全部已有和用户修改。
3. 以实际工作树为准：SG-0 已验证；SG-1 已产生部分代码但完整 gate 尚未通过。
4. 禁止 `git reset`、`git checkout --`、删除现有文件、重新生成整个工程或把 `PARTIAL_NOT_VERIFIED` 当成零实现。

## Checkpoint SG-0A-TECH-BASELINE

### HTTP Router

- 添加并实际使用 `github.com/go-chi/chi/v5`。
- 整个进程只装配一个 `chi.Router`；healthz、readyz、metrics 与后续 WebSocket route 使用同一 Router。
- 保留 `net/http.Server`、HTTP/gRPC 双 listener 和优雅停机；`main` 只依赖最终 `http.Handler`。
- 删除生产代码中的 `http.NewServeMux`，不增加自研 Router interface。
- 只使用必要 middleware；WebSocket route 禁止全局 Timeout，日志禁止原始 RequestURI/query。

### Session Store

- `STORE_MODE` 默认 `redis`，只允许 `redis|memory`；删除 `auto`。
- Redis 模式要求 `REDIS_URL`，格式错误或启动 PING 失败时 fail fast。
- 运行中 Redis 失败时返回 unavailable，不切换 Store。
- MemoryStore 只保留给 shared contract tests 和显式本地单进程开发；必须暴露 local/degraded，不进入 SG-4 部署清单。

### OpenTelemetry

- 使用 OpenTelemetry Go 初始化并在关闭时 flush/shutdown tracer provider。
- 覆盖当前可用 HTTP、gRPC、SessionManager/SessionStore；SG-2/SG-3 再覆盖 WebSocket 与 Runtime Adapter。
- 通过 context 传播；不得把 ticket、query、credential、terminal/VNC 内容、ciphertext 或无界高基数字段写入 span。
- 提供 exporter 配置时格式错误或初始化失败必须 fail fast；没有真实 collector 时 export smoke 为 `not_verified`。

### Dependency timing

- SG-0A 引入并实际使用 chi 和 OpenTelemetry。
- SG-2 引入并实际使用 coder/websocket、client-go 与 remotecommand。
- SG-3 引入并实际使用 kubevirt.io/client-go。
- 不为让 `go.mod` 提前出现而加入未使用依赖；最终根 module 的直接依赖必须覆盖全部冻结技术栈。

### Gates

至少验证并记录：

```text
go mod tidy
go test -race ./...
go vet ./...
make check
git diff --check
```

增加 Router method/404/405/recovery、Redis default/fail-fast/no-fallback、显式 memory local/degraded、tracing init/shutdown/敏感字段排除测试。用 `rg` 或等价门禁确认生产代码不存在 `http.NewServeMux`、gorilla/websocket、`STORE_MODE=auto` 或 Redis 失败选择 MemoryStore 的路径。

完成后记录 `SG-0A TECH_BASELINE_CORRECTED`，再继续 SG-1。

## Continue SG-1 through SG-4

- 先审计并完成现有 SG-1；不得重写已经符合 v1.2 的 SessionManager/Store contract。
- SG-2 强制使用 chi 中央 Router、coder/websocket 和 `k8s.io/client-go/tools/remotecommand`。
- SG-3 强制使用 `kubevirt.io/client-go`。
- SG-4 部署固定 Redis，禁止 memory/auto，包含 Prometheus 与 OpenTelemetry 配置并保持原 RBAC/NetworkPolicy/securityContext 门禁。
- 每个 checkpoint 运行完整回归、更新 status/record；无真实依赖继续标 `not_verified`。

## Unchanged deny scope

禁止 Git staging/commit/tag/push/remote 修改、ANI 修改、SSH、部署、集群 mutation、ANI-GW-1、CONSOLE-1 和 LIVE-1。完成 SG-4 后停止于 `LOCAL_VERIFIED / MANIFEST_VERIFIED`。
