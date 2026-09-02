# Codex Goal 提示词

> 使用方式：一次只复制一个 `/goal`。先替换所有 `<...>` 占位符；未替换时不要启动。每个 Goal 都是一个有界 Work Package，不会自动授权下一个批次、GitHub 写入、SSH 或部署。

官方 OpenAI 文档建议 Goal 明确单一目标、停止条件、必读材料、验证工件、检查点和进度日志：<https://learn.chatgpt.com/use-cases/follow-goals>。

## 公共约束

以下规则已写入各提示词，不需要再额外补充：

- 用户手工提交 GitHub；禁止 `git add`、commit、配置 remote、push 和 tag。
- 禁止 SSH、部署或改变集群状态；真实 smoke 没有授权时只能记 `not_verified`。
- 每批必须创建或更新 `docs/execution/records/<ID>.md`，记录 baseline、allow/deny paths、命令、结果、未验证项和回滚。
- 完成后只更新 `docs/execution/status.md`；不会自动开始下一个 Goal。

## SG-0：仓库骨架与内部契约

启动前替换：

```text
<MODULE_PATH>  例如 github.com/<owner>/ani-session-gateway；必须是用户已决定的最终路径
```

```text
/goal 在 /home/chabking/workspace/ani-session-gateway 完成且只完成 Work Package SG-0：建立根实现 Go module、独立 api Go submodule、内部 CreateSession gRPC 契约、双 listener、配置校验、health/readiness/metrics、容器构建骨架。持续工作直到 SG-0 的本地验证全部通过；完成后停止，不开始 SG-1。

固定输入：
- MODULE_PATH=<MODULE_PATH>
- 设计合同：docs/design/realtime-session-gateway-p0-v1.1.md
- 当前状态：docs/execution/status.md

开始前完整读取 AGENTS.md、CLAUDE.md、设计合同和状态文件；确认 active_work_package 为空、MODULE_PATH 已替换且工作树不存在未知冲突。把 active_work_package 更新为 SG-0，并创建 docs/execution/records/SG-0.md。

允许修改：
- api/go.mod、api/proto/**、api/gen/**
- 根 go.mod/go.sum、buf.yaml、buf.gen.yaml
- cmd/session-gateway/**、internal/config/**、internal/transport/grpc/**、internal/observability/**
- Dockerfile、Makefile、README.md、.github/workflows/**
- docs/websocket-protocol.md、docs/execution/status.md、docs/execution/records/SG-0.md

禁止：
- Redis/Memory SessionStore 业务实现、WebSocket session route、client-go、KubeVirt、ANI 仓库改动和 Kubernetes 部署清单
- 修改 v1.1 设计决定，新增延期能力
- git add/commit/remote/push/tag、SSH、部署或读取敏感凭据

必须实现：
1. 根 module 为 <MODULE_PATH>，api submodule 为 <MODULE_PATH>/api；API submodule 只依赖 protobuf/grpc 最小集合。
2. proto 使用 v1.1 的单一 CreateSession interface、oneof mode、typed WorkloadKind 和稳定字段编号；生成代码进入 api/gen/session/v1。
3. HTTP listener 暴露 /healthz、/readyz、/metrics；gRPC listener 注册 SessionService，但 SG-0 未实现的 CreateSession 明确返回 Unimplemented，不能伪造 URL。
4. 配置对 PUBLIC_WS_BASE_URL、ALLOWED_ORIGINS、地址、时间窗口和 key file path 做 fail-fast 解析；不在日志输出敏感值。
5. Dockerfile 以 non-root 运行并兼容只读 root filesystem。
6. Makefile 提供 generate、lint、test、vet、build、check-generated 和 diff-check 等可重复入口。

验证：
- buf lint
- protobuf 生成漂移检查
- 分别在根 module 与 api submodule 运行 go test ./... 和 go vet ./...
- 构建二进制和容器文件静态检查
- 缺少 PUBLIC_WS_BASE_URL、ALLOWED_ORIGINS 或 key path 时启动失败测试
- HTTP/gRPC listener 与 health probe 测试
- git diff --check

首个 API tag 尚不存在，因此不要伪造 buf breaking passed；记录为“首 tag 发布后建立基线”。不要发布 api/v0.1.0，只在 SG-0 记录中给用户写出手工提交和发布命令。

完成条件：全部本地门禁通过、记录文件含真实命令和结果、status 标为 SG-0 LOCAL_VERIFIED 并把 active_work_package 清空。缺少依赖或环境时穷尽本地可行验证后标 not_verified；只有 MODULE_PATH 未提供、设计冲突或需要扩大权限时才报告阻断。
```

## SG-1：SessionManager 与 Redis/Memory Store

启动前替换 `<SG0_COMMIT>` 为用户提交 SG-0 后的精确 commit。

```text
/goal 在 /home/chabking/workspace/ani-session-gateway 完成且只完成 Work Package SG-1：实现 SessionManager、ticket 加密/摘要、幂等状态机，以及 MemoryStore/RedisStore 的同一契约。持续工作直到 SG-1 全部门禁通过；完成后停止，不开始 SG-2。

基线：SG-0 精确 commit=<SG0_COMMIT>。先读取 AGENTS.md、CLAUDE.md、v1.1 设计、status 和 SG-0 record；确认 HEAD 精确匹配且工作树没有未知冲突。设置 active_work_package=SG-1，创建 docs/execution/records/SG-1.md。

允许修改：internal/session/**、internal/store/memory/**、internal/store/redis/**、internal/config 中 SG-1 配置、对应测试、Makefile/go.mod/go.sum、status 和 SG-1 record。

禁止：WebSocket route、Kubernetes/KubeVirt runtime、部署清单、ANI 改动、设计范围外重构、git/remote/SSH/部署动作。

必须实现并通过同一 contract suite：
- crypto/rand 至少 32 bytes ticket、SHA-256 digest、AES-256-GCM ciphertext；key 文件读取正好 32 个 raw bytes，不再次 base64 解码。
- request fingerprint 覆盖 tenant/subject/target/mode/options，不含 request ID、时间和随机值。
- CreateOrGet：issued 且未过期时同键同 fingerprint 重放同一 ticket；不同 fingerprint conflict；claimed/expired/closed 返回 failed precondition。
- ClaimAndReserve：ticket 比对、expiry、状态转换、全局/subject 容量和 lease 在 Redis 中单次原子完成；Memory 语义一致。
- CloseAndRelease 幂等；重复关闭不产生负计数；过期 lease 和模拟进程崩溃可回收容量。
- STORE_MODE 只在启动时选择，运行中 Redis 失败绝不热切 Memory。

至少验证：100 并发 claim 仅一个成功、同键重放、fingerprint conflict、claim 后重放、ticket tombstone、全局/subject 容量、lease 回收、重复 close、Redis 启动/运行中断、auto 固定选择、明文 ticket 搜索为零、race test、go test ./...、go vet ./...、git diff --check。Redis 真实 integration test 若本机依赖不可用必须记 not_verified，不能用 fake 冒充。

完成时写 SG-1 record，status 标 SG-1 LOCAL_VERIFIED 或明确 not_verified 项，清空 active_work_package。不得自动开始 SG-2。
```

## SG-2：WebSocket 与 Pod exec

```text
/goal 在 /home/chabking/workspace/ani-session-gateway 完成且只完成 Work Package SG-2：实现一次性 ticket WebSocket transport、JSON terminal protocol、Pod resolver 和 Kubernetes exec Adapter。持续工作直到本地/集成门禁通过；完成后停止，不开始 SG-3，也不访问真实集群。

基线：使用用户批准的 SG-1 精确 commit=<SG1_COMMIT>。先读取 AGENTS.md、CLAUDE.md、v1.1 设计、status、SG-0/SG-1 records并确认干净基线。设置 active_work_package=SG-2，创建 SG-2 record。

允许修改：internal/transport/websocket/**、internal/runtime/kubernetes/**、internal/session 为接线所需的最小改动、配置/指标/测试、go.mod/go.sum、Makefile、docs/websocket-protocol.md、status 和 SG-2 record。

禁止：KubeVirt、部署清单、ANI、Console、真实 SSH/集群、GitHub 写入和延期能力。

必须实现：Origin 精确白名单、upgrade 前 ClaimAndReserve、query/ticket 日志脱敏、frame 上限、stdin/resize 校验、ping/pong、idle/max-duration、背压、脱敏 error+1011、graceful close；namespace 确定性推导、tenant+instance 双 label、Running/Ready/non-terminating Pod 选择、container 歧义拒绝；ExecStream 隐藏 remotecommand 类型并支持 stdin/stdout/stderr/resize/wait/close。

测试使用 fake ExecRuntime 和受控 Kubernetes HTTP/SPDY seam 分层验证，不要求 WebSocket 测试穿透真实 provider。至少覆盖并发 claim、错误 Origin、过期 ticket、ticket 重放、超限、慢客户端、帧过大、非 Ready Pod、跨 tenant、container 歧义、TTY stderr 合并、resize、超时和日志脱敏。运行 go test -race ./...、go vet ./...、生成/协议检查和 git diff --check。真实 kind/集群 smoke 明确记 not_verified。

完成时更新 SG-2 record/status并清空 active_work_package，不开始 SG-3。
```

## SG-3：KubeVirt serial 与 VNC

```text
/goal 在 /home/chabking/workspace/ani-session-gateway 完成且只完成 Work Package SG-3：实现 KubeVirt v1.8.2 serial console 与 VNC Adapter，以及 serial JSON bridge 和 VNC binary RFB bridge。持续工作直到本地/集成门禁通过；完成后停止，不开始 SG-4，也不访问真实集群。

基线：用户批准的 SG-2 精确 commit=<SG2_COMMIT>。先完成标准读取和基线检查，设置 active_work_package=SG-3并创建 SG-3 record。

允许修改：internal/runtime/kubevirt/**、WebSocket transport 中 serial/VNC 分流的最小改动、配置/指标/测试、go.mod/go.sum、协议文档、status 和 SG-3 record。

禁止：把 RFB 包进 JSON/base64、把 noVNC 静态站点放入 Gateway、Kubernetes 部署清单、ANI/Console 修改、SSH/真实集群、GitHub 写入。

必须实现：连接前读取并验证 VMI namespace/name/Running；provider 使用 plain.kubevirt.io；serial 用 ByteStream 转 JSON stdout/stdin；VNC 使用透明 binary frames；任一路径都必须继承一次性 claim、容量 lease、deadline、背压、关闭与脱敏不变量。

至少覆盖：serial 双向字节、binary frame bit-for-bit、RFB handshake fixture、键鼠字节不被转换、ticket 重放、跨 tenant、非 Running VMI、provider error 脱敏、连接关闭释放 lease、go test -race ./...、go vet ./...、git diff --check。真实 KubeVirt smoke 没有本 Goal 的集群授权，必须记 not_verified。

完成时更新 SG-3 record/status并清空 active_work_package，不开始 SG-4。
```

## SG-4：部署、RBAC 与 NetworkPolicy 清单

```text
/goal 在 /home/chabking/workspace/ani-session-gateway 完成且只完成 Work Package SG-4：生成并静态验证 Session Gateway 的 Deployment、两个 Service、ServiceAccount、最小 RBAC、NetworkPolicy、Secret 引用、probes/resources/securityContext 和 graceful shutdown 配置。完成后停止；禁止实际部署。

基线：用户批准的 SG-3 精确 commit=<SG3_COMMIT>。先完成标准读取和基线检查，设置 active_work_package=SG-4并创建 SG-4 record。

允许修改：deploy/kubernetes/**、配置示例、Makefile/验证脚本、README、status 和 SG-4 record。只在验证暴露真实缺陷时允许对应用代码做最小修复并记录理由。

禁止：kubectl apply/patch/delete/rollout、SSH、读取 kubeconfig/Secret、修改 ANI、配置固定公网 IP、声称 NodePort 30081 已可用、GitHub 写入。

清单必须：gRPC ClusterIP 与 WS NodePort 分离；memory/auto 默认 replicas=1；gRPC ingress 同时限制 ani-system namespace 与 ani-gateway pod selector；egress 明确 DNS、Redis、Kubernetes API/virt-api；ClusterRole 只含 pods get/list、pods/exec create、VMI get、console/vnc subresource get；禁止 Secret read、Pod delete、VM write、wildcard；non-root、read-only rootfs、drop capabilities、seccomp、资源限制、probes、termination grace 和 key Secret volume 均闭合。NodePort 30081 只是 planned value并带部署前查重门禁。

运行可用的 YAML/schema、Kustomize/Helm（若实际采用）、kubectl client dry-run、RBAC 静态正反向测试、NetworkPolicy selector/egress 测试、go test/vet 和 git diff --check。没有集群时 kubectl auth can-i、外部 9090 拒绝和 NodePort 可达都记 not_verified。

完成时 status 标 SG-4 MANIFEST_VERIFIED（不是 deployed/live），写 record并清空 active_work_package。不得自动启动 ANI-GW-1 或 LIVE-1。
```

## ANI-GW-1：ANI Gateway gRPC 接入

启动前替换全部占位符。`<API_VERSION>` 必须是用户已手工发布且可解析的固定 `api/v0.1.0` 或精确 pseudo-version。

```text
/goal 在 /home/chabking/workspace/ANI 完成且只完成 Work Package ANI-GW-1：保持既有 Core REST 产品契约兼容，把 exec/console 的占位 URL 签发替换为 ani-session-gateway 内部 gRPC client，并拆分 InstanceObservability 与 InstanceSessionIssuer seam。持续工作直到 ANI 本地门禁全部通过；完成后停止，不修改 Console、不部署。

固定输入：
- ANI_BASELINE=<ANI_APPROVED_EXACT_COMMIT，当前已知候选为 963bc88836c54a1b09cf100b37eb2f2cb2a5a4be>
- SESSION_API_MODULE=<MODULE_PATH>/api
- SESSION_API_VERSION=<API_VERSION>
- SESSION_GATEWAY_REPO_COMMIT=<SG4_EXACT_COMMIT>

先完整读取 ANI/AGENTS.md、CLAUDE.md、ANI-DOCS-INDEX.md、repo/CURRENT-SPRINT.md、ANI-06-开发计划.md Section 零、相关 OpenAPI/ports/adapter/router/frontend现状，以及 Session Gateway v1.1 设计和 SG-0～SG-4 records。确认 ANI HEAD 精确等于批准 baseline；不得用 moving main/latest 替代。记录并保护开始时所有已存在的修改和未跟踪文件，尤其不得修改、删除、暂存或提交与本批无关的文档。

允许修改：
- repo/api/openapi/v1.yaml 及由兼容性 additive 修改必需的生成物
- repo/pkg/ports/instance_observability.go、一个新的 instance_session port 文件，以及直接相关的 Local/Prometheus Adapter 最小拆分
- repo/pkg/bootstrap/** 中必需接线
- repo/services/ani-gateway/internal/router/instances.go、直接相关测试、新 session gRPC client
- repo/services/ani-gateway runtime/main/go.mod/go.sum
- production-shaped 配置中 Session Gateway 地址及移除占位 EXEC_BASE_URL
- ANI 规则要求的 development record、索引、CURRENT-SPRINT 和 ANI-06 更新

禁止：
- 修改既有 REST path、删除/改型既有字段、引入 client-go/KubeVirt、注册 WebSocket route、代理字节流
- 修改 Console、Session Gateway 仓库、其它服务或无关重构
- 本机 replace、复制 proto、浮动版本/latest
- git add/commit/remote/push/tag、SSH、部署、切流或读取敏感凭据

必须实现：
1. OpenAPI 只做兼容性增量：exec/console 声明 409/422/429/503/500；console request 新增可选 idempotency_key；重新生成所需 schema/SDK/authz并通过兼容门禁。
2. InstanceObservability 移除 CreateExecSession/CreateConsoleSession；新增深 interface InstanceSessionIssuer。Local 开发 Adapter 可实现两个 interface，Prometheus Adapter 不再签发 session。
3. 固定版本 gRPC Adapter 传 tenant、subject、真实 instance ID、record.Name、typed kind、request ID 和 mode options；绝不传 namespace、Pod/VMI URL或 credential。
4. exec 检查 kind、running、command、rows/cols；console 保留 VM/running/protocol 检查。auth/RBAC denied 请求绝不调用 gRPC。
5. console 优先透传客户端 idempotency_key；当前兼容客户端省略时生成内部 UUID并明确不保证跨 HTTP 重试。
6. gRPC 错误按 v1.1 映射；未配置 SESSION_GATEWAY_GRPC_ADDR 或依赖不可用时 real provider 返回 503，不生成假 URL。
7. 成功响应维持既有 schema并设置 dev_profile.real_provider=true；Local profile 明确 real_provider=false。

至少验证：focused handler/client/runtime tests、denied-never-calls、请求映射、全部错误映射、OpenAPI YAML/生成漂移、Core API compatibility、gateway authz、make test、make validate-architecture、make validate-doc-entrypoints、相关仓库门禁和 git diff --check。Session Gateway 未运行时允许使用 in-process fake gRPC server验证 contract；不得把它记为 live。

完成时新增 ANI development record，列出精确 Session API version、ANI baseline、命令与结果、not_verified live 项和回滚方式；按 ANI 规则更新索引/CURRENT-SPRINT/ANI-06。不要提交或部署，不要开始 CONSOLE-1。
```

