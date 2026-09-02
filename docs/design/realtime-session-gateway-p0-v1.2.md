# ANI Realtime Session Gateway P0 实施方案 v1.2

> 日期：2026-09-02  
> 状态：设计已闭合，可按 SG-0～SG-4、ANI-GW-1、CONSOLE-1、LIVE-1 分批实现  
> 范围：Kubernetes Pod terminal、KubeVirt serial console、KubeVirt VNC/noVNC  
> 仓库：新建独立 `ani-session-gateway` 仓库；现有 `ANI` 仓库只做 Gateway、Console 和部署集成  
> 非目标：消息通知、告警站内信、NATS 事件总线、mTLS、统一 Envoy/Ingress、会话录屏、文件传输
> 执行权限：本文是设计合同，不授权 Git remote、commit、push、tag、SSH、部署、切流或删除；这些动作必须由对应 Goal 提示词或用户单独明确授权

> 修订说明：v1.2 补录用户在最初设计阶段已选定的技术栈，并纠正 v1.1 中 `STORE_MODE=auto` 自动降级到 MemoryStore 的冲突。v1.1 保留为历史版本；执行与验收以 v1.2 为准。

## 0. v1.2 闭合决策

本节是 v1.2 的强制决策摘要；后文与本节冲突时以本节为准。

1. **P0 环境级别**：`network_only`、明文内部 gRPC 和 `ws://` 只允许隔离测试环境，不构成生产身份认证或传输安全，不得据此声明 production-ready。HTTPS Console 必须使用 `wss://`，否则启动或前端连接必须 fail closed。
2. **仓库与发布权**：本地仓库固定为 `/home/chabking/workspace/ani-session-gateway`。GitHub remote 与 Go module 根路径由用户创建 GitHub 仓库后提供；Agent 不得猜测。用户采用手工 GitHub 提交，因此默认禁止 `git add`、commit、配置 remote、push 和发布 tag。
3. **Go 契约模块**：实现使用根 Go module；对 ANI 发布的 proto/client 使用独立 `api` Go submodule，模块路径为 `<MODULE_PATH>/api`，版本 tag 使用 `api/v0.1.0`。ANI 只依赖该 API submodule，避免把 `client-go`、KubeVirt 等实现依赖带入 Gateway 的模块图。
4. **Core API 兼容性**：既有 REST path、成功响应字段和字段类型不变；允许 additive 修改 OpenAPI，为 exec/console 补充真实会返回的 `409/422/429/503/500`，并为 console request 增加可选 `idempotency_key`。Console P0 前端必须始终发送该键；旧客户端省略时可创建新会话，但不保证网络重试重放同一结果。
5. **时间窗口分离**：ticket 可 claim 时间默认 `60s`；已建立会话最大时长默认 `15m`；空闲超时默认 `10m`；幂等 tombstone 默认保留 `15m`。四者不得复用一个配置或含糊地称为 session TTL。
6. **幂等确定性**：同键同 fingerprint 且仍为 `issued` 时返回首次 session 与同一 ticket；同键不同 fingerprint 返回 conflict；ticket 已 claim、ticket 已过期或会话已关闭时，同键重放返回 failed precondition，不签发新 ticket。`request_fingerprint` 覆盖 tenant、subject、target、mode 与所有 mode options，不包含 request ID。
7. **claim 与容量**：生产、部署和默认运行时 Session Store 固定为 Redis；ticket 比对、`issued -> claimed`、全局容量预占、subject 容量预占和租约写入必须在一次原子操作中完成。MemoryStore 只允许作为 contract tests 和显式本地开发 Adapter，不得成为部署默认值或 Redis 失败时的自动降级路径。关闭、过期和进程崩溃后的租约回收必须幂等，不能留下永久占用。
8. **stream interface**：exec 使用带 stdin/stdout/stderr/resize/wait 语义的 `ExecStream`；serial/VNC 使用透明 `ByteStream`。禁止用一个要求所有模式实现 resize/exit 的通用 stream interface。
9. **Secret 文件格式**：`TICKET_ENCRYPTION_KEY_FILE` 挂载文件内容固定为 32 个原始 bytes。Kubernetes Secret `data` 的 base64 只属于 YAML/API 编码，volume 挂载后已解码；进程不得再次 base64 解码。
10. **测试环境授权**：`~/.ssh/config` 中存在 host `ani` 只是环境线索，不是授权。只有 LIVE-1 Goal 明确写出允许的 host、namespace、资源、fixture、回滚和 Git/部署权限后，Agent 才可 SSH 或改变集群状态。
11. **冻结技术基线**：P0 必须使用本节列出的依赖；不得用标准库替代、同类库或自研 framework 静默替换。依赖版本由根 `go.mod`/`go.sum` 锁定，升级必须通过兼容性与回归门禁。

### 0.1 冻结技术基线

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

约束：

- 整个进程只装配一个 `chi.Router`，`main` 只接收最终 `http.Handler`；不得在 chi 外再封装自研 Router framework。
- `coder/websocket` 只存在于 WebSocket transport 实现；不得替换为 gorilla/websocket 或自研帧层。
- `remotecommand`、KubeVirt client、Redis client 分别封装在 `ExecRuntime`、`VMConsoleRuntime`、`SessionStore` Adapter 后，不得泄露到 transport 或外部 interface。
- Prometheus 与 OpenTelemetry 类型不得泄露到业务 interface。OpenTelemetry 覆盖 gRPC、WebSocket 生命周期、SessionStore 与 Runtime Adapter；span 不得包含 ticket、query、credential、终端/VNC 内容、ciphertext 或无界高基数字段。
- 不给 WebSocket route 使用全局 timeout middleware，不记录原始 RequestURI/query。提供了无效 tracing/exporter 配置时启动失败；未配置真实 collector 的 export smoke 记为 `not_verified`。

## 1. 结论

P0 新建并独立部署一个 Go 进程 `ani-session-gateway`，同一进程同时监听：

- `:9090`：仅集群内访问的 gRPC 控制面，供现有 `ani-gateway` 创建短期会话；
- `:8080`：通过独立 NodePort 暴露的 WebSocket 数据面，供浏览器连接终端、serial console 和 VNC。

现有 Core OpenAPI 路径和 schema 保持不变：

```text
POST /api/v1/instances/{instance_id}/exec
POST /api/v1/instances/{instance_id}/console
```

`ani-gateway` 继续负责用户身份认证、租户隔离、RBAC、实例存在性与状态检查。通过检查后，它调用 Session Gateway 的内部 gRPC 创建一次性会话，并把 `ws_url` 或 `connect_url` 原样映射为现有 OpenAPI 响应。

Session Gateway 使用自己的 ServiceAccount，通过 `client-go` 和 `kubevirt.io/client-go` 访问 Kubernetes/KubeVirt。高权限 SDK、WebSocket 连接和字节流全部留在这个独立进程，不加入现有 Core domain/service 实现。

P0 暂不做内部 mTLS。gRPC 只通过 ClusterIP 暴露，并由 NetworkPolicy 同时使用 namespace selector 与 pod selector 限制为 `ani-system` 中的 `ani-gateway` 可访问。浏览器 WebSocket 的一次性 ticket、过期、原子 claim 和 Origin 校验不能取消。该模式只允许隔离测试环境；任何生产部署必须另行补齐服务身份认证与 TLS 决策。

生产和部署状态固定使用 Redis：Redis 配置缺失、格式错误或启动连通性检查失败时必须启动失败；运行中 Redis 失败时新签发/claim 返回 unavailable，已有字节流按其独立生命周期继续。MemoryStore 只用于 contract tests 和显式本地单进程开发，不得自动接管 Redis 状态。

## 2. 当前仓库事实

### 2.1 已存在且继续复用

Core OpenAPI 已经定义：

- `createInstanceExecSession`，返回 `InstanceExecSession.ws_url`；
- `createInstanceConsoleSession`，返回 `InstanceConsoleSession.connect_url/url`；
- exec 权限为 `scope:instances:exec`；
- console 权限为 `scope:instances:console`。

Console 已经存在：

- `TerminalTab.tsx`：创建 exec session、建立 WebSocket、使用 xterm.js 处理 stdin/resize/stdout/stderr/exit/error；
- `ConsoleTab.tsx`：创建 VM console session，但当前只是 `window.open(connect_url)`，尚无真正的 noVNC/serial viewer。

真实环境已证明 KubeVirt `console` 和 `vnc` subresource 能完成 HTTP 101 upgrade，并使用 `plain.kubevirt.io` 子协议传输字节流；该证据只证明 provider 可达，不代表产品会话链路已实现。

### 2.2 当前不能直接使用的部分

现有 Local/Prometheus observability Adapter 的 `CreateExecSession` 和 `CreateConsoleSession` 只生成 URL 并存入本地 map，没有任何 WebSocket listener，也没有连接 Kubernetes exec 或 KubeVirt subresource。

production-shaped 清单中的 `INSTANCE_OBSERVABILITY_EXEC_BASE_URL` 指向 `ani-gateway`，但 `ani-gateway` 当前没有对应的 WebSocket route。这一配置只能产生不可连接的占位 URL。

当前 `ani-gateway` Service 是 NodePort：

```text
NodePort 30080 -> ani-gateway:8080
```

所以在不增加 Envoy/Ingress 的情况下，新进程不能复用 `30080` 按 `/api/v1/realtime` 分流。P0 必须使用另一个 NodePort。

当前内部 gRPC client 使用 `insecure.NewCredentials()`，服务端也没有 `grpc.Creds(...)`，因此本方案暂缓 mTLS 与仓库现状一致。

## 3. 最终部署拓扑

```text
Browser / ANI Console
  |
  | 1. POST /api/v1/instances/{id}/exec|console
  v
NodeIP:30080
  |
  v
ani-gateway
  |- Bearer/API key authentication
  |- scope:instances:exec or scope:instances:console
  |- tenant + instance lookup
  |- kind/state validation
  |
  | 2. plaintext gRPC CreateSession
  v
ani-session-gateway-grpc.ani-system.svc:9090 (ClusterIP only)
  |- issue one-time ticket
  |- SessionStore: Redis
  |- derive tenant namespace
  `- return public ws_url

Browser
  |
  | 3. WebSocket + one-time ticket
  v
NodeIP:30081 (planned; deploy前必须查重)
  |
  v
ani-session-gateway-ws:8080
  |- atomic ticket claim
  |- origin/expiry/capacity checks
  |- Pod exec -> Kubernetes API
  |- serial/VNC -> KubeVirt virt-api
  `- structured lifecycle logs + Prometheus metrics + OpenTelemetry traces
```

创建两个 Kubernetes Service：

1. `ani-session-gateway-grpc`：`ClusterIP`，只暴露 `9090`；
2. `ani-session-gateway-ws`：`NodePort`，只暴露 `8080`，计划使用 `30081`。

不要创建一个同时包含 gRPC 和 WebSocket 端口的 NodePort Service，否则 Kubernetes 会同时给内部 gRPC 端口分配外部 NodePort。

实验环境使用：

```text
PUBLIC_WS_BASE_URL=ws://<可达节点地址>:30081/api/v1/realtime
ALLOWED_ORIGINS=http://<Console地址>:30080
```

如果 Console 改成 HTTPS，必须同时把公开连接改成 `wss://`。浏览器不会允许 HTTPS 页面连接 `ws://`。TLS 可以由后续 Envoy/Ingress 终止，也可以由 Session Gateway 自身终止；不进入本 P0。

## 4. 仓库职责

### 4.1 新仓库 `ani-session-gateway`

建议初始目录：

```text
ani-session-gateway/
|- api/go.mod
|- api/proto/session/v1/session.proto
|- api/gen/session/v1/*.pb.go
|- buf.yaml
|- buf.gen.yaml
|- cmd/session-gateway/main.go
|- internal/config/config.go
|- internal/session/model.go
|- internal/session/manager.go
|- internal/session/store.go
|- internal/store/memory/store.go
|- internal/store/redis/store.go
|- internal/transport/grpc/server.go
|- internal/transport/websocket/handler.go
|- internal/transport/websocket/exec_protocol.go
|- internal/runtime/kubernetes/exec.go
|- internal/runtime/kubernetes/pod_resolver.go
|- internal/runtime/kubevirt/console.go
|- internal/observability/metrics.go
|- deploy/kubernetes/deployment.yaml
|- deploy/kubernetes/services.yaml
|- deploy/kubernetes/rbac.yaml
|- deploy/kubernetes/networkpolicy.yaml
|- docs/websocket-protocol.md
|- Dockerfile
|- Makefile
|- go.mod
`- README.md
```

该仓库是内部 gRPC proto 和 WebSocket 协议的真实来源。根 module 承载实现；`api` submodule 只包含 proto、生成 client 和 protobuf/grpc 最小依赖。用户创建 GitHub remote 并给出 `<MODULE_PATH>` 后，生成的 Go client 以 `api/v0.1.0` 或固定 API submodule pseudo-version 发布。ANI 仓库通过固定版本依赖 `<MODULE_PATH>/api`；不得在 ANI 的 `go.mod` 中提交指向开发者本机路径的 `replace`，也不得复制 proto 维持联调。

本地联调可以在开发者自己的 `go.work` 中把两个 checkout 加入 workspace，但该本地 workspace 配置不提交。

### 4.2 现有 `ANI` 仓库

只承担：

- 保持 Core OpenAPI 不变；
- 把 session 创建从合成 URL 改为内部 gRPC 调用；
- 保留现有 REST authentication/RBAC；
- Console 接入真实 exec WebSocket、serial viewer 和 noVNC；
- production-shaped 部署清单加入 Session Gateway 地址；
- 增加跨进程 live gate。

`ani-gateway` 不引入 `client-go` 或 KubeVirt SDK，不代理 WebSocket 字节流，也不新增 `/api/v1/realtime` route。

## 5. Session Gateway 的 Module 与 seam

### 5.1 外部 interface：内部 gRPC

只提供一个深 interface：`CreateSession`。exec、serial 和 VNC 通过 `oneof` 表达，不拆成多套生命周期方法。

建议 proto：

```proto
syntax = "proto3";

package ani.session.v1;

import "google/protobuf/timestamp.proto";

service SessionService {
  rpc CreateSession(CreateSessionRequest) returns (CreateSessionResponse);
}

message CreateSessionRequest {
  string request_id = 1;
  string idempotency_key = 2;
  Principal principal = 3;
  Target target = 4;
  oneof mode {
    ExecOptions exec = 5;
    VMConsoleOptions vm_console = 6;
  }
}

message Principal {
  string tenant_id = 1;
  string subject_id = 2;
}

message Target {
  string instance_id = 1;
  string workload_name = 2;
  WorkloadKind workload_kind = 3;
}

enum WorkloadKind {
  WORKLOAD_KIND_UNSPECIFIED = 0;
  WORKLOAD_KIND_CONTAINER = 1;
  WORKLOAD_KIND_GPU_CONTAINER = 2;
  WORKLOAD_KIND_SANDBOX = 3;
  WORKLOAD_KIND_VM = 4;
}

message ExecOptions {
  string container = 1;
  repeated string command = 2;
  bool tty = 3;
  int32 rows = 4;
  int32 cols = 5;
}

message VMConsoleOptions {
  enum Protocol {
    PROTOCOL_UNSPECIFIED = 0;
    PROTOCOL_SERIAL = 1;
    PROTOCOL_VNC = 2;
  }
  Protocol protocol = 1;
  string requested_protocol = 2;
}

message CreateSessionResponse {
  string session_id = 1;
  string connect_url = 2;
  google.protobuf.Timestamp expires_at = 3;
  bool replayed = 4;
}
```

映射规则：

| Core 请求 | 内部 mode | browser 数据协议 |
|---|---|---|
| exec | `ExecOptions` | JSON text frames |
| `console` / `serial` | `PROTOCOL_SERIAL` | JSON text frames + xterm |
| `vnc` / `novnc` | `PROTOCOL_VNC` | transparent binary RFB + noVNC |

`requested_protocol` 用于把外部请求值原样带回响应；runtime 只需要理解 serial 和 VNC 两种真实模式。

### 5.2 内部 seam：SessionStore

```go
type SessionStore interface {
    CreateOrGet(ctx context.Context, key IdempotencyKey, candidate Session) (Session, bool, error)
    ClaimAndReserve(ctx context.Context, id SessionID, ticketDigest [32]byte, now time.Time, limits ClaimLimits) (SessionLease, error)
    CloseAndRelease(ctx context.Context, id SessionID, leaseID LeaseID, reason CloseReason, now time.Time) error
}
```

接口语义是其一部分：

- `CreateOrGet` 必须原子实现幂等；同键同 fingerprint 且 session 仍为 `issued` 时返回首次 session，同键不同 fingerprint 返回 conflict；已 claim、已过期或已关闭的同键请求返回 failed precondition，不能签发新 ticket；
- `ClaimAndReserve` 必须原子完成 ticket 常量时间比对、ticket 过期检查、`issued -> claimed`、全局与 subject 容量预占、带过期时间的 lease 写入；Redis 实现使用 Lua script 或等价的单原子事务；
- 一个 session 最多 claim 一次；
- store 保存 ticket 的 SHA-256 digest，以及用于幂等重放的 AEAD ciphertext；不得保存明文 ticket；
- 同键重放时 SessionManager 使用挂载的 ticket key 解密并返回首次 ticket；claim 成功后立即清除 ciphertext；
- `CloseAndRelease` 必须幂等关闭并释放对应 lease；重复 close、连接异常、进程退出或 lease 超时不能造成负计数；Redis claim 前先清理已过期 lease，Memory Adapter 在 contract tests 和显式本地开发中使用同一状态机语义；
- `request_fingerprint` 是 tenant、subject、target、mode 和全部 mode options 的确定性编码 SHA-256，不包含 request ID、创建时间或随机 ticket；
- Redis 和 Memory Adapter 必须通过同一组 contract tests。

`SessionStore` 是真实 seam：生产 Adapter 是 RedisStore；MemoryStore 是保持同一 interface 语义的测试/显式本地开发 Adapter。MemoryStore 不得成为生产 Adapter、部署默认值或 Redis 故障回退。

### 5.3 内部 seam：Runtime

```go
type ExecRuntime interface {
    OpenExec(ctx context.Context, target ExecTarget, size TerminalSize) (ExecStream, error)
}

type VMConsoleRuntime interface {
    OpenSerial(ctx context.Context, target VMTarget) (ByteStream, error)
    OpenVNC(ctx context.Context, target VMTarget) (ByteStream, error)
}

type ExecStream interface {
    WriteStdin([]byte) error
    ReadStdout([]byte) (int, error)
    ReadStderr([]byte) (int, error)
    Resize(TerminalSize) error
    Wait() (exitCode int, err error)
    Close() error
}

type ByteStream interface {
    io.Reader
    io.Writer
    io.Closer
}
```

`ExecStream` 只服务 Pod exec；`ByteStream` 只服务 serial/VNC 的透明字节桥接。二者都不向 transport 暴露 Kubernetes `remotecommand` 或 KubeVirt client 类型，也不要求 VNC 实现无意义的 resize/exit 方法。

## 6. 会话模型与安全不变量

Session 至少保存：

```text
session_id
ticket_digest
ticket_ciphertext
tenant_id
subject_id
instance_id
workload_name
workload_kind
mode
container/command/tty/rows/cols
request_fingerprint
state: issued|claimed|closed
created_at
ticket_expires_at
expires_at
claimed_at
closed_at
close_reason
```

强制不变量：

1. ticket 使用 `crypto/rand` 生成至少 32 bytes，再进行 base64url 编码；
2. 原始 ticket 只出现在首次或幂等重放返回的 `connect_url` 中；store 只持有 digest 和 AES-256-GCM ciphertext；
3. 禁止把完整 URL、query string、ticket 或 Kubernetes credential 写日志；
4. ticket 一次性使用，WebSocket upgrade 前原子 claim；
5. 默认 ticket claim 窗口为 60 秒；默认已建立 session 最大时长为 15 分钟；默认 idle timeout 为 10 分钟；幂等 tombstone 保留 15 分钟；
6. 默认总连接上限 100、每 subject 上限 5，超限拒绝 claim；Redis 模式为所有副本共享的全局上限，Memory 模式为唯一进程上限；
7. 只接受配置中的 Origin；生产配置禁止 `*`；
8. Session Gateway 自己从 `tenant_id` 推导 `ani-tenant-<tenant-id>`，gRPC caller 不得直接指定任意 namespace；
9. Pod 只能通过以下两个 label 同时解析：

   ```text
   ani.kubercloud.io/tenant-id=<tenant_id>
   ani.kubercloud.io/instance=<workload_name>
   ```

10. exec 仅允许 `container`、`gpu_container`、`sandbox`；VM console 仅允许 `vm`；
11. 只选择非 terminating、`Running` 且 Ready 的 Pod；滚动发布存在多个候选时选择最新 Ready Pod并记录其名称；
12. 如果指定 container 不存在，拒绝会话；未指定时只允许 workload 中恰好有一个业务 container，不能悄悄进入 sidecar；
13. server 设置 handshake timeout、read/write deadline、最大 frame 大小、ping/pong 和慢客户端背压上限；
14. 收到 SIGTERM 后停止签发和接受新连接，在 termination grace period 内关闭现有 session。

P0 使用：

```text
GET /api/v1/realtime/sessions/{session_id}?ticket=<one-time-ticket>
```

由于浏览器原生 WebSocket 不能设置任意 Authorization header，P0 把短期 ticket 放在 URL。必须关闭带 query 的 access log，并确保错误响应不回显 URL。后续可以改用同站 cookie 或 WebSocket subprotocol，但不应阻塞 P0。

幂等返回只在 session 仍为 `issued` 且 ticket 尚未过期时解密 ciphertext。claim 成功后立即清除 ciphertext；此后同一幂等键返回 failed precondition。ticket 过期但尚未被 claim 时，store 清除 ciphertext、保留 tombstone 到 `IDEMPOTENCY_TTL`，同键请求仍不得创建第二个 session。

## 7. 浏览器 WebSocket 协议

### 7.1 exec 与 serial

客户端发送 text frame：

```json
{"type":"stdin","data":"ls -la\r"}
{"type":"resize","rows":30,"cols":120}
```

服务端发送 text frame：

```json
{"type":"stdout","data":"..."}
{"type":"stderr","data":"..."}
{"type":"exit","code":0}
{"type":"error","code":"RUNTIME_STREAM_FAILED","message":"terminal stream failed"}
```

约束：

- `stdin.data` 最大 64 KiB；
- `rows/cols` 必须为正数并设置合理上限；
- TTY 模式下 Kubernetes 会合并 stdout/stderr，服务端统一发 `stdout`；
- 不把 Kubernetes 原始错误、Pod spec、节点信息或 credential 返回浏览器；
- transport 错误后发送脱敏 error frame，再以 1011 close。

现有 `TerminalTab.tsx` 已兼容该协议，只需补 close code/error state 和 resize listener 清理。

### 7.2 VNC/noVNC

VNC WebSocket 是透明的 binary RFB bridge：

```text
noVNC RFB <-> browser WebSocket binary frames <-> KubeVirt VNC stream
```

此路径不能套 exec JSON envelope，也不能把 VNC 内容转成 base64 JSON。

Console 使用 `@novnc/novnc` 创建 `RFB` 实例。`ConsoleTab` 不再 `window.open(ws://...)`，而是在 Dialog/全屏容器中实例化 VNC viewer。这样 ticket 不会再次进入浏览器地址栏。

serial console 使用现有 xterm 交互方式，可抽取 TerminalTab 的 WebSocket/xterm 绑定代码为私有 hook，但不要为了 VNC 和 terminal 建立一套大而通用的 transport abstraction。

## 8. Kubernetes 与 KubeVirt Adapter

### 8.1 Pod exec

实现使用：

- `rest.InClusterConfig()`；
- `kubernetes.NewForConfig()`；
- `remotecommand.NewSPDYExecutor()`；
- `PodExecOptions`，透传 container、command、stdin/stdout/stderr、tty；
- `remotecommand.TerminalSizeQueue` 处理 resize。

命令默认值继续沿用 Core 请求的 `[/bin/sh]`。Session Gateway 不提供任意 HTTP 参数直接覆盖 command；command 只来自经过 RBAC 的 `ani-gateway` gRPC 请求。

### 8.2 KubeVirt serial/VNC

KubeVirt client 版本必须和真实集群当前 KubeVirt minor 版本对齐。当前 live evidence 为 `v1.8.2`，第一版依赖应锁定同一版本，不使用浮动 latest。

实现使用 KubeVirt client 的 VMI subresource stream：

- serial -> `virtualmachineinstances/console`；
- VNC -> `virtualmachineinstances/vnc`；
- provider 侧使用 `plain.kubevirt.io`；
- browser 侧按本方案分别使用 JSON terminal protocol 或 binary RFB。

连接前再次读取 VMI，确认 namespace、name 和 Running 状态。Gateway 的状态检查是产品检查，Session Gateway 的检查是高权限执行前的防御性检查，两者不能互相替代。

## 9. Session Store 运行策略

配置：

| 配置 | 允许副本数 | Redis 启动失败 | 运行中 Redis 失败 |
|---|---:|---|---|
| `STORE_MODE=redis` | `>=1` | 启动失败 | 新签发/claim 返回 unavailable；已有流继续 |
| `STORE_MODE=memory` | `1`，仅测试/显式本地开发 | 不连接 Redis | 不适用 |

`STORE_MODE` 默认且部署固定为 `redis`。不存在 `auto` 模式；Redis 启动失败不得创建 MemoryStore。否则旧 ticket、幂等记录和 claim 状态会分裂，部署也会静默失去多副本语义。

RedisStore 的容量不是进程内计数。它使用 session lease 表达已 claim 的活动连接；`ClaimAndReserve` 在一个原子脚本中先删除 `expires_at <= now` 的 lease，再检查全局和 subject 数量，最后转换 session 状态并写入 lease。WebSocket 正常结束或异常结束调用 `CloseAndRelease`；进程崩溃时由 lease 的 session 最大时长自动回收。MemoryStore 在 contract tests/显式本地开发中复用同一状态机，但只允许单副本。

Memory 模式明确不保证：

- Pod 重启后的未 claim ticket；
- 滚动升级期间连续性；
- 多副本负载均衡；
- HA。

`/readyz` 在启动时降级到 Memory 后仍返回 200，因为单副本功能可用，但 body、日志和指标必须暴露 degraded：

```text
ani_session_store_info{mode="redis|memory"} 1
ani_session_store_degraded 0|1
```

在 `redis` 模式下，Redis 运行中不可用时 readiness 返回失败；显式本地 `memory` 模式不检查 Redis，但必须暴露 local/degraded 状态，且不得用于部署验收。

## 10. 鉴权与网络安全

### 10.1 P0 信任链

```text
User credential
  -> ani-gateway authentication
  -> Core operation policy / RBAC
  -> trusted internal gRPC request
  -> one-time browser ticket
  -> Kubernetes/KubeVirt ServiceAccount
```

暂不实现 gRPC mTLS 或 service JWT。内部 gRPC 的 P0 模式命名为 `network_only`，部署必须满足：

- gRPC 端口只有 ClusterIP；
- 没有 Ingress/NodePort/hostPort；
- NetworkPolicy 的 ingress 同时要求 namespace label `kubernetes.io/metadata.name=ani-system` 与 Pod label `app.kubernetes.io/name=ani-gateway`；
- NetworkPolicy 显式定义 Session Gateway 到 DNS、Redis、Kubernetes API/virt-api 的最小 egress，不依赖集群默认放行；
- Session Gateway 不接受 browser credential，也不自行解析 ANI JWT；
- WebSocket 只接受已由内部 gRPC 签发的一次性 ticket。

NetworkPolicy 只提供 L3/L4 隔离，不能证明调用方 ServiceAccount 身份；能在 `ani-system` 创建并伪造 label 的主体仍可能调用 gRPC。因此 `network_only` 只接受为隔离测试环境的已知风险。“暂缓 mTLS”不等于公开 WebSocket 无鉴权，也不等于 Session Gateway ServiceAccount 可以使用 cluster-admin。

### 10.2 最小 RBAC

Session Gateway 独立 ServiceAccount：`ani-session-gateway`。

```yaml
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["pods/exec"]
    verbs: ["create"]
  - apiGroups: ["kubevirt.io"]
    resources: ["virtualmachineinstances"]
    verbs: ["get"]
  - apiGroups: ["subresources.kubevirt.io"]
    resources:
      - "virtualmachineinstances/console"
      - "virtualmachineinstances/vnc"
    verbs: ["get"]
```

不得授予 Pod create/update/delete、Secret read、VM lifecycle write、Node proxy 或通配符权限。

由于租户 namespace 是动态的，P0 可使用上述最小 ClusterRole + ClusterRoleBinding；应用层必须同时执行 namespace 推导和双 label 校验。后续若要进一步收敛，可在租户创建流程中生成 namespace RoleBinding。

## 11. 健康检查、日志与指标

HTTP listener 同时提供：

```text
GET /healthz  - 进程存活，不探测 Kubernetes/Redis
GET /readyz   - server 已监听、store 可用、Kubernetes config 已构建
GET /metrics  - Prometheus
GET /api/v1/realtime/sessions/{id} - WebSocket upgrade
```

结构化日志事件：

```text
session_issued
session_replayed
session_claimed
session_connected
session_closed
session_failed
store_degraded
```

只记录：request ID、session ID、tenant ID、subject ID、instance ID、mode、选中的 Pod/VMI、时长、字节数、close reason。禁止记录 ticket、query、stdin/stdout/VNC 内容、Bearer token、ServiceAccount token。

最低指标：

```text
ani_session_create_total{mode,result}
ani_session_active{mode}
ani_session_duration_seconds{mode}
ani_session_bytes_total{mode,direction}
ani_session_claim_total{result}
ani_session_runtime_errors_total{mode,code}
ani_session_store_info{mode}
ani_session_store_degraded
```

P0 不引入 NATS。NATS 未来只用于把 issued/started/ended/failed 生命周期事件可靠投递给审计消费者，不能传输 terminal/VNC 字节，也不能替代 Redis。只有同时交付 durable consumer 和落库时才加入。

## 12. 配置清单

### 12.1 Session Gateway

| 环境变量 | 默认值 | P0 说明 |
|---|---|---|
| `HTTP_ADDR` | `:8080` | WebSocket、health、metrics |
| `GRPC_ADDR` | `:9090` | 内部 CreateSession |
| `PUBLIC_WS_BASE_URL` | 无 | 必填；NodePort 外部可达地址 |
| `ALLOWED_ORIGINS` | 无 | 必填；逗号分隔，禁止 `*` |
| `STORE_MODE` | `redis` | `redis|memory`；部署固定 redis，memory 仅测试/显式本地开发 |
| `REDIS_URL` | 无 | redis 模式必填；缺失、格式错误或启动 PING 失败时 fail fast |
| `TICKET_ENCRYPTION_KEY_FILE` | 无 | 必填；Secret volume 挂载后内容必须是 32 个原始 bytes，进程不再 base64 解码 |
| `TICKET_TTL` | `60s` | 签发后允许 claim 的窗口 |
| `SESSION_MAX_DURATION` | `15m` | claim 后到强制关闭的最长时长 |
| `IDEMPOTENCY_TTL` | `15m` | 幂等结果/tombstone 保留窗口 |
| `MAX_ACTIVE_SESSIONS` | `100` | Redis 模式全局上限；Memory 模式唯一进程上限 |
| `MAX_ACTIVE_PER_SUBJECT` | `5` | Redis 模式跨副本 subject 上限；Memory 模式唯一进程上限 |
| `WS_MAX_MESSAGE_BYTES` | `65536` | exec/serial client frame 上限 |
| `WS_HANDSHAKE_TIMEOUT` | `10s` | upgrade 上限 |
| `WS_IDLE_TIMEOUT` | `10m` | 无流量关闭 |
| `SHUTDOWN_GRACE_PERIOD` | `25s` | 小于 Pod termination grace |
| `OTEL_SERVICE_NAME` | `ani-session-gateway` | OpenTelemetry service name |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | 无 | 可选 OTLP endpoint；一旦提供，格式错误或初始化失败时 fail fast |

`PUBLIC_WS_BASE_URL` 启动时必须校验：scheme 只能是 `ws`/`wss`，不得带 query/userinfo，path 固定以 `/api/v1/realtime` 结尾。生产标识或 HTTPS Console 配置下 `ws://` 必须拒绝启动/连接。ticket encryption key 文件必须读取为正好 32 bytes；Kubernetes Secret `data` 在 volume mount 时已经完成 base64 解码。多副本必须挂载同一 Secret。P0 不实现在线 key rotation。

### 12.2 ani-gateway

新增：

| 环境变量 | 示例 |
|---|---|
| `SESSION_GATEWAY_GRPC_ADDR` | `ani-session-gateway-grpc.ani-system.svc.cluster.local:9090` |
| `SESSION_GATEWAY_GRPC_TIMEOUT` | `5s` |

删除 production-shaped 清单里的占位配置：

```text
INSTANCE_OBSERVABILITY_EXEC_BASE_URL
```

如果 `SESSION_GATEWAY_GRPC_ADDR` 未配置，真实 provider 下 exec/console 创建必须返回 dependency unavailable，不能继续生成一个看似有效但无法连接的 URL。Local dev profile 可以继续使用显式 local session issuer，但响应必须保留 `real_provider=false`。

## 13. ANI Gateway 改造

### 13.1 拆开 observability 与 session seam

当前 `InstanceObservability` 同时包含日志、事件、指标、安全事件和 session 创建，职责混合。本批新增：

```go
type InstanceSessionIssuer interface {
    CreateExecSession(context.Context, InstanceExecSessionCreateRequest) (InstanceExecSessionRecord, error)
    CreateConsoleSession(context.Context, InstanceConsoleSessionCreateRequest) (InstanceConsoleSessionRecord, error)
}
```

并从 `InstanceObservability` 移除两个 Create 方法。Local Adapter 可以同时实现两个 interface；Prometheus Adapter 只实现 observability，不再生成 session URL。

`instanceAPI` 分别持有：

```text
observability ports.InstanceObservability
sessions      ports.InstanceSessionIssuer
```

这是本次采用的 seam：指标/日志 provider 的变化不再影响实时会话，Session Gateway client 的变化也不再污染 Prometheus Adapter。

### 13.2 请求映射

Gateway 调用内部 gRPC 时传递：

- request ID；
- 外部 exec 的 `idempotency_key`；console 优先使用新增的可选 `idempotency_key`，当前 Console 必须生成并发送；兼容旧客户端省略时 Gateway 生成一次内部 UUID，但这种请求不承诺跨 HTTP 重试重放；
- tenant ID；
- principal subject ID；
- ANI instance ID；
- `record.Name` 作为 workload name；
- `record.Kind`；
- exec options 或归一化后的 VM console mode。

Gateway 不传 Kubernetes bearer token、namespace、Pod name、VMI URL 或 provider client。

exec handler 增加当前缺失的产品检查：

- kind 必须是 `container|gpu_container|sandbox`；
- state 必须是 `running`；
- command 为空时使用 `[/bin/sh]`；
- rows/cols 做正数和上限检查；
- session client 未配置时 fail closed。

console handler保留现有 VM/running/protocol 检查，并把 `console|serial` 映射为 serial，把 `vnc|novnc` 映射为 VNC。

### 13.3 gRPC 错误映射

| gRPC code | Gateway HTTP |
|---|---:|
| `InvalidArgument` | 400 |
| `Unauthenticated` / `PermissionDenied` | 403 |
| `NotFound` | 404 |
| `AlreadyExists` | 409 |
| `FailedPrecondition` | 422 |
| `ResourceExhausted` | 429 |
| `Unavailable` / `DeadlineExceeded` | 503 |
| 其他 | 500 |

响应继续使用现有 `ErrorResponse` envelope。Core OpenAPI path、成功响应和既有字段不变；本批必须做 additive 契约修改，为 exec/console 声明上述真实错误响应，并为 console request 增加可选 `idempotency_key`。生成的 authz、SDK/schema 与兼容性门禁必须同步；真实响应中的 `dev_profile` 必须明确 `real_provider=true`，不能沿用占位 Adapter 的标记。

## 14. Console 改造

### 14.1 TerminalTab

保留现有 POST 和 xterm 主流程，补充：

- `ws_url` 的 `ws/wss` scheme 校验；
- WebSocket close code 到 UI 状态映射；
- `exit` 后禁止继续发送 stdin；
- resize handler/disposable 清理；
- 组件卸载时发送正常 close；
- 错误信息不显示带 ticket 的 URL。

### 14.2 ConsoleTab

新增两个私有 viewer：

```text
SerialConsoleDialog  - xterm + exec/serial JSON protocol
VNCConsoleDialog     - @novnc/novnc RFB + binary WebSocket
```

行为：

- `console|serial` 创建 session 后打开 `SerialConsoleDialog`；
- `vnc|novnc` 创建 session 后打开 `VNCConsoleDialog`；
- 每次新的 console 连接生成 `idempotency_key`，同一次 POST 的网络重试复用该键；
- Dialog 关闭时主动断开 WebSocket/RFB；
- session 过期后提示重新申请；
- 不再对 `ws://` URL 调用 `window.open()`。

需要增加 `@novnc/novnc` 依赖及相应 TypeScript 类型处理。不要把 noVNC 静态站点部署到 Session Gateway；viewer 属于 Console，Session Gateway 只代理 RFB 字节流。

## 15. 编码批次

以下批次按顺序执行。`SG-0` 合入后，`SG-1/SG-2` 与 `ANI-GW-1` 可以基于已发布的 proto 并行，但产品 live gate 必须按最终顺序收口。

### SG-0：独立仓库骨架与内部契约

目标：新仓库能构建、启动两个 listener，并准备可供 ANI 引用的独立 API submodule。

改动：

- 在已存在的本地 Git 仓库中创建根实现 module 与 `api` submodule；`<MODULE_PATH>` 必须由用户在 GitHub 仓库创建后明确提供，Agent 不得猜测；
- 加入 proto/buf generation；
- 实现 config fail-fast validation；
- 启动 HTTP 和 gRPC server；
- 加入 `/healthz`、`/readyz`、`/metrics`；
- Dockerfile 使用非 root、只读 root filesystem 兼容的镜像；
- 生成并验证待发布的 `api/v0.1.0`；remote 配置、commit、push 和 tag 由用户手工执行，除非后续 Goal 另行明确授权。

完成标准：

- `buf lint`、生成漂移检查、根 module 与 API submodule的 `go test ./...`、`go vet ./...` 通过；首个 API tag 发布后再把它固定为后续 `buf breaking` 基线；
- 缺少 `PUBLIC_WS_BASE_URL`/`ALLOWED_ORIGINS` 时启动失败；
- HTTP 和 gRPC health probe 可达；
- ANI 可导入生成的 `SessionServiceClient`。

### SG-0A：冻结技术基线纠偏

目标：在保留 SG-0 成果和已产生 SG-1 修改的前提下，补齐 v1.2 技术合同并纠正初版 `http.ServeMux`、Redis 自动降级与缺失 tracing；完成后继续原 `SG-P0-LOCAL`，不另开实现 Goal。

改动：

- 用单一 `chi.Router` 替换 `http.NewServeMux` 装配，保留 `net/http.Server`、双 listener 与优雅停机；
- 初始化并关闭 OpenTelemetry tracer provider，覆盖当前可用的 HTTP/gRPC/SessionStore 路径；真实 collector 不可用时 export smoke 记为 `not_verified`；
- 配置默认固定 `STORE_MODE=redis`，删除 `auto` 和 Redis 失败自动降级；MemoryStore 只保留给 contract tests/显式本地开发；
- 更新依赖和测试，但不提前加入尚未实际使用的 coder/websocket、client-go 或 KubeVirt 依赖；它们分别在 SG-2/SG-3 引入；
- 在执行记录中保留 SG-0 原验证事实，并单独记录本次设计勘误、受影响门禁和回滚。

完成标准：

- 根 module 直接依赖并实际使用 `github.com/go-chi/chi/v5` 与 OpenTelemetry Go；
- 生产代码不再使用 `http.NewServeMux`，不存在 `STORE_MODE=auto` 或 Redis 失败选择 MemoryStore 的路径；
- Router 行为、method/404/405、panic recovery、Redis fail-fast、显式 memory local/degraded、tracing 初始化/关闭与敏感字段排除测试通过；
- `go mod tidy`、`go test -race ./...`、`go vet ./...`、生成/契约检查和 `git diff --check` 通过；
- status/record 标记 `SG-0A TECH_BASELINE_CORRECTED`，然后从工作树真实进度继续 SG-1。

### SG-1：SessionManager + Redis Store

目标：完成签发、幂等、ticket digest、原子 claim、过期和关闭，不接 provider。

改动：

- 实现 session model/manager；
- 实现 ticket 生成、SHA-256 digest、AES-256-GCM 静态加密和幂等解密；
- 保留 `MemoryStore` 作为 contract tests/显式本地开发 Adapter；
- 实现 `RedisStore`，原子操作使用 Lua script 或等价事务；
- 实现 `STORE_MODE=redis|memory` 启动选择，默认和部署固定为 redis；删除 auto 与 Redis 失败自动降级；
- 两个 Adapter 运行同一套 contract tests；
- 增加并发 claim、同键不同 fingerprint、claim 后重放、ticket 过期 tombstone、全局/subject 容量、lease 回收、Redis 中断测试。

完成标准：

- 100 个并发 claim 中只有一个成功；
- 同一幂等键同请求返回同一 session；
- 同键不同请求返回 conflict；
- claim 后或 ticket 过期后的同键请求返回 failed precondition，不创建第二个 session；
- store 中找不到明文 ticket，ciphertext 可由相同 Secret 完成幂等重放；
- Redis 多副本语义下容量预占与 claim 原子，过期 lease 可回收且重复 close 不产生负计数；
- Redis 配置或启动连通性失败时 fail fast，运行中失败不切换；
- memory 模式指标明确 local/degraded，且部署清单不得启用。

### SG-2：WebSocket transport + Pod exec

目标：浏览器可通过一次性 ticket 进入真实 Pod terminal。

改动：

- 实现 WebSocket route、Origin 和 ticket claim；
- 实现 JSON terminal protocol、ping/pong、deadline、backpressure；
- 实现双 label Pod resolver；
- 实现 client-go remotecommand exec 和 resize；
- 增加 fake Kubernetes API/unit tests 和 kind/state/container 拒绝测试；
- 增加 ticket 60s claim 窗口、15m 最大会话、10m idle timeout 的可控时钟测试，以及本地 kind 或真实集群 smoke test。

完成标准：

- xterm 输入 `printf ani-terminal-ok` 能收到输出；
- resize 到 `120x30` 可被远端 TTY 观察；
- ticket 重放被拒绝；
- 错误 Origin、过期 ticket、非 Ready Pod、歧义 container 均 fail closed；
- 日志不包含 ticket 和终端内容。

### SG-3：KubeVirt serial + VNC

目标：真实 VM serial 和 noVNC 均可操作。

改动：

- 接入与集群匹配版本的 kubevirt client；
- 实现 serial JSON bridge；
- 实现 VNC binary bridge；
- 校验 VMI Running；
- 测试 binary frame 不被 JSON/base64 转换；
- 复用既有 KubeVirt live fixture 做真实 smoke。

完成标准：

- serial 能发送换行并读取 guest 输出；
- VNC 完成 RFB handshake 并渲染画面；
- VNC 键盘/鼠标输入可到达 VM；
- ticket 重放、跨 tenant target、非 Running VMI 被拒绝。

### SG-4：部署、RBAC、NetworkPolicy

目标：部署 Session Gateway 后，只有 WS 对浏览器暴露，gRPC 保持集群内。

改动：

- Deployment、两个 Service、ServiceAccount、最小 ClusterRole/Binding；
- NetworkPolicy；
- NetworkPolicy 使用 namespace+pod 双 selector，并显式定义 DNS、Redis、Kubernetes API/virt-api egress；
- probes、resources、Pod security context、graceful shutdown；
- Redis Secret 引用；
- NodePort 查重说明与部署前检查；
- 部署固定 `STORE_MODE=redis`；副本数可从 1 起步并由 Redis 保持跨副本会话语义，禁止部署 memory/auto。

完成标准：

- `kubectl auth can-i` 证明所需四类权限允许；
- `kubectl auth can-i` 证明 Secret read、Pod delete、VM update 被拒绝；
- 集群外不能访问 9090；
- 非 ani-gateway Pod 不能访问 gRPC；
- NodePort 30081 可从 Console 所在浏览器网络访问；
- Pod restart 后服务恢复，Redis 中未 claim session 与幂等状态仍可验证。

### ANI-GW-1：Core Gateway gRPC 接入

目标：保留现有 OpenAPI/RBAC，把合成 URL 替换成真实 Session Gateway 调用。

主要文件：

```text
repo/pkg/ports/instance_observability.go
repo/pkg/ports/instance_session.go                         [NEW]
repo/pkg/adapters/runtime/local_instance_observability_service.go
repo/pkg/adapters/runtime/prometheus_instance_observability.go
repo/pkg/bootstrap/deps.go
repo/pkg/bootstrap/server.go
repo/services/ani-gateway/internal/router/instances.go
repo/services/ani-gateway/internal/router/instances_test.go
repo/services/ani-gateway/internal/router/router.go
repo/services/ani-gateway/internal/router/session_grpc_client.go [NEW]
repo/services/ani-gateway/internal/router/session_grpc_client_test.go [NEW]
repo/services/ani-gateway/instance_session_runtime.go       [NEW]
repo/services/ani-gateway/instance_session_runtime_test.go  [NEW]
repo/services/ani-gateway/main.go
repo/services/ani-gateway/go.mod
repo/services/ani-gateway/go.sum
```

完成标准：

- Core OpenAPI 仅有兼容性 additive diff：exec/console 错误响应与 console `idempotency_key`；兼容性门禁通过；
- auth/RBAC middleware 仍先于 handler；
- denied 请求不会调用 Session Gateway；
- gRPC 请求包含 tenant、subject、instance ID、workload name/kind 和 request ID；
- real provider 未配置 Session Gateway 时返回 503，不生成假 URL；
- exec/console 的成功响应保持现有 schema；
- gRPC error mapping tests 通过；
- exec/console 真实响应的 `dev_profile.real_provider=true`，不沿用占位 Adapter 标记；
- Gateway 不新增 client-go/KubeVirt 依赖；
- Gateway 不注册 WebSocket route。

### CONSOLE-1：Terminal/serial/noVNC 客户端

目标：现有 Console 中三条产品路径都能操作。

主要文件：

```text
repo/frontends/console/package.json
repo/frontends/console/pnpm-lock.yaml
repo/frontends/console/src/features/instance-observability/TerminalTab.tsx
repo/frontends/console/src/features/instance-observability/ConsoleTab.tsx
repo/frontends/console/src/features/instance-observability/SerialConsoleDialog.tsx [NEW]
repo/frontends/console/src/features/instance-observability/VNCConsoleDialog.tsx    [NEW]
repo/frontends/console/src/features/instance-observability/useTerminalSocket.ts   [NEW, 仅在复用确实减少重复时]
```

完成标准：

- terminal、serial、VNC 三条 UI 状态机有组件测试；
- VNC 关闭 Dialog 会释放 RFB 和 WebSocket；
- ticket 不进入浏览器地址栏、错误文案或前端日志；
- `pnpm type-check`、`pnpm lint`、`pnpm build` 通过；
- HTTPS 页面配到 `ws://` 时给出明确配置错误，不进行连接。

### LIVE-1：跨仓库产品链路门禁

目标：证明不是 provider probe，而是浏览器产品链路真实可用。

顺序：

```text
Console/API credential
  -> ani-gateway NodePort 30080
  -> auth/RBAC
  -> Session Gateway gRPC
  -> one-time ticket
  -> Session Gateway NodePort 30081
  -> Kubernetes/KubeVirt stream
```

必须覆盖：

1. Pod terminal stdin/stdout + resize；
2. VM serial read/write；
3. VM VNC RFB handshake、画面和至少一次输入；
4. 无 `scope:instances:exec` 返回 403，且无 session_issued；
5. 无 `scope:instances:console` 返回 403，且无 session_issued；
6. tenant A 不能打开 tenant B 的 workload；
7. ticket 重放失败；
8. ticket 过期失败；
9. Redis 配置缺失或启动连接失败时进程 fail fast；显式本地 memory 模式单独标记 local/degraded，且不计入部署通过；
10. Redis 模式运行中断开时新会话 fail closed，已有流行为有证据；
11. Session Gateway restart 后无假成功；
12. 日志/evidence 中不含 credential、ticket、终端内容和 VNC 内容。
13. 同一 console 幂等键在 claim 前重放同一结果，claim 后返回 failed precondition；
14. Redis 模式并发 claim 不突破全局/subject 上限，模拟进程崩溃后 lease 到期可恢复容量；
15. `network_only` 仅在明确的隔离测试 namespace 中运行，非匹配 namespace 或 Pod selector 不能访问 9090。

完成后才能声明：

```text
Pod terminal / KubeVirt serial / KubeVirt VNC product path live passed
```

不能据此声明 HA、mTLS、统一 TLS 入口或 full platform production ready。

## 16. 测试分层

### 16.1 新仓库

```text
unit:
- config validation
- session state machine
- ticket hashing/constant-time compare
- protocol frame validation
- target/namespace derivation

contract:
- RedisStore and local/test MemoryStore shared suite
- generated gRPC client/server round trip

integration:
- real Redis
- fake Kubernetes API + exec transport seam
- WebSocket concurrent claim/backpressure

live:
- real Pod exec
- real KubeVirt serial
- real KubeVirt VNC
```

### 16.2 ANI 仓库

```text
unit:
- handler validation
- gRPC request mapping
- gRPC error mapping
- denied request never calls client

frontend:
- TerminalTab state machine
- SerialConsoleDialog cleanup
- VNCConsoleDialog lifecycle

live:
- 30080 control plane + 30081 data plane end-to-end
```

## 17. 发布和回滚

### 17.1 发布顺序

1. 用户手工创建 GitHub 仓库、配置 remote、提交并发布 Session Gateway API submodule tag `api/v0.1.0`；Agent 复核 tag/commit 后 ANI 才能依赖；
2. 部署 Session Gateway，先验证 health、RBAC、Redis fail-fast/持久语义和 NodePort；
3. 发布带 gRPC client 的 `ani-gateway`，配置 `SESSION_GATEWAY_GRPC_ADDR`；
4. 验证 REST 创建 session 和原生 WebSocket smoke；
5. 发布 Console terminal/serial/noVNC；
6. 执行 LIVE-1；
7. 归档脱敏 evidence 和 development record。

部署 Session Gateway 但尚未切换 Gateway 时，不影响现有 REST 请求；切换 Gateway 后若新服务不可用，session 创建明确返回 503。

### 17.2 回滚

推荐回滚顺序：

1. 回滚 Console 到上一版本；
2. 回滚 `ani-gateway` 或取消 `SESSION_GATEWAY_GRPC_ADDR`；
3. 删除 Session Gateway NodePort Service，停止新增外部连接；
4. 等待/终止现有 Session Gateway Pod；
5. 最后删除 Deployment/RBAC。

不要把 Gateway 回滚到“生产环境继续生成假 WebSocket URL”的状态。若必须保留旧代码，应通过配置让 session 创建 fail closed。

## 18. 明确延期项

以下内容不进入本轮编码：

- gRPC mTLS、SPIFFE/SPIRE、service JWT；
- Envoy/Ingress 同域名和统一 `wss://`；
- Redis 多副本和 Session Gateway HA live gate；
- NATS/JetStream session lifecycle 审计投递；
- terminal/VNC 内容录制；
- 会话共享、接管、暂停、恢复；
- 消息通知、告警推送和站内信；
- 浏览器文件上传/下载；
- SSH、RDP、SPICE；
- 任意 Kubernetes resource terminal。

## 19. 开工入口

第一项编码任务直接执行 `SG-0`。它的提交应只包含独立仓库骨架、proto、双 listener、health/metrics、配置校验和生成 client，不提前写 Redis、client-go、KubeVirt 或 ANI Gateway 改动。

`SG-0` 完成后，先发布 proto/client 固定版本，再开始 `SG-1` 和 `ANI-GW-1`。这样两个仓库围绕同一个内部 interface 开发，不需要用复制 proto 或本机 `replace` 维持联调。

每个 ANI feature batch 完成后仍按仓库规则更新 development record、索引、CURRENT-SPRINT 和开发计划；真实 provider 能力在 LIVE-1 通过前只能标记为 contract/local/integration ready。

## 20. 环境信息与人工解锁

`~/.ssh/config` 中的 host `ani` 是候选测试环境。本文不授权读取敏感凭据、SSH、部署或改变集群状态。LIVE-1 开始前，用户必须在 Goal 提示词中明确：允许使用的 host、namespace、fixture、NodePort、允许创建/更新/删除的资源、是否允许 rollout 现有 Gateway/Console、失败回滚动作及 evidence 保存位置。未获得该授权时只能完成 local/contract/integration ready，不能执行 LIVE-1。
