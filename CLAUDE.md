# CLAUDE.md

本文是 ANI Session Gateway 的稳定 Agent 合同。动态任务、临时发现和逐批证据不得堆入本文。

## 1. 当前权限状态

```text
设计已闭合：是
实现已启动：否
remote/commit/push/tag：未授权
SSH/部署/切流/删除：未授权
```

规划文档不是执行授权。只有用户用 `/goal` 或等价明确指令启动一个批次，才允许在该批次 allow paths 内改变状态。

## 2. 权威来源

1. `docs/design/realtime-session-gateway-p0-v1.1.md`：P0 设计、接口、安全不变量、批次和验收标准。
2. `docs/execution/status.md`：当前批次、状态、基线和阻断。
3. `docs/execution/records/<ID>.md`：单批次范围、命令、证据、未验证项和回滚。
4. 当前用户 Goal：本次授权范围；与设计冲突或扩大范围时必须停止。

## 3. Module 与 seam

- 外部 Module 只有内部 gRPC `CreateSession` interface；exec、serial、VNC 共享会话生命周期，不拆成多套外部 interface。
- `SessionStore` 是真实 seam，只有 Memory 与 Redis 两个生产 Adapter；二者运行同一 contract suite。
- `ExecRuntime` 与 `VMConsoleRuntime` 是内部 seam；生产 Adapter 连接 Kubernetes/KubeVirt，测试 Adapter 只替换该 seam。
- exec 使用 `ExecStream`；serial/VNC 使用透明 `ByteStream`。不得重新合并为强迫所有模式实现 resize/exit 的通用 interface。
- HTTP/WebSocket/gRPC transport 不能持有 Kubernetes 或 KubeVirt SDK 类型。

## 4. 不可破坏的不变量

- ticket 至少 32 random bytes，只在 connect URL 中出现；日志、指标、错误、evidence 不得包含 ticket、query、credential 或会话内容。
- `CreateOrGet`、claim、容量 lease 和 close 语义以 v1.1 设计为准；Redis 多副本不能退化为进程内计数。
- `network_only`、明文 gRPC 和 `ws://` 只允许隔离测试环境，不得声明 production-ready。
- Session Gateway 不解析 ANI 用户凭据；ANI Gateway 负责用户认证、tenant、RBAC、实例存在性/kind/state 检查。
- Session Gateway 自行推导 namespace，并在高权限动作前执行 tenant 与 workload 双 label 校验。
- 不引入 NATS、mTLS、统一 Ingress、录屏、文件传输、HA 声明或其它延期能力。

## 5. Git 与跨仓库纪律

- 用户手工提交 GitHub：默认不执行 `git add`、commit、配置 remote、push 或 tag。
- Go module 根路径必须由用户提供，Agent 不得从 ANI remote 猜测。
- ANI 只依赖 `<MODULE_PATH>/api` 的固定 tag/commit pseudo-version；禁止提交本机 `replace` 或复制 proto。
- 每个 Goal 记录开始 commit、结束 diff、验证命令和未验证项。无真实依赖时写 `not_verified`，禁止伪造 passed。

## 6. 验证与完成

- 一次只执行一个有界 Work Package；至少包含 allow/deny paths、成功条件、停止条件、证据和回滚。
- 优先写 interface/contract 测试，再写实现；测试通过后运行该批次的完整 lint、test、vet、生成漂移和 `git diff --check`。
- Goal 只在全部成功条件真实成立时完成。缺少 module path、发布版本、真实依赖或外部权限时报告阻断，不自行扩大授权。

