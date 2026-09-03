# ANI Session Gateway

ANI 的实时会话执行进程，P0 覆盖 Kubernetes Pod terminal、KubeVirt serial console 与 KubeVirt VNC/noVNC。根实现 module 固定为 `github.com/zhangzhe-ctrl/ani-session-gateway`，独立契约 module 为 `github.com/zhangzhe-ctrl/ani-session-gateway/api`。

## 当前状态

```text
设计：CLOSED（v1.2；v1.1 为历史）
实现：Connected Session 架构已合入 main，测试环境 DEPLOYED_SMOKE_VERIFIED
当前 Work Package：SG-CONNECTED-SESSION-DEPLOY-20260903
GitHub remote：origin/main 已更新到包含 Connected Session 架构的合并提交
Go module path：github.com/zhangzhe-ctrl/ani-session-gateway（用户已明确）
测试环境部署：ani-system 已部署不可变镜像并通过 rollout、探针和安全失败链路冒烟
```

## 权威读取顺序

1. `AGENTS.md`
2. `CLAUDE.md`
3. `docs/design/realtime-session-gateway-p0-v1.2.md`
4. `docs/execution/status.md`
5. 当前 Goal 对应的 `docs/execution/records/<ID>.md`

## 实施批次

```text
SG-0  仓库骨架、API submodule、双 listener
SG-0A 冻结技术基线纠偏（chi、OpenTelemetry、Redis fail-fast）
SG-1  SessionManager、Memory/Redis Store
SG-2  WebSocket transport、Pod exec
SG-3  KubeVirt serial、VNC
SG-4  Kubernetes 部署、RBAC、NetworkPolicy

ANI-GW-1  在 ANI 仓库接入内部 gRPC
CONSOLE-1 前端 terminal/serial/noVNC
LIVE-1    隔离测试环境产品链路门禁
```

可直接使用的 Goal 提示词见 `docs/execution/goal-prompts.md`。

本地验证入口为 `make tools generate check`，其中 `make manifests` 对部署清单、最小 RBAC、NetworkPolicy、Secret mount 与 ticket key 长度门禁做静态验证。部署前人工步骤见 `deploy/README.md`。进程启动要求显式配置 `PUBLIC_WS_BASE_URL`、`ALLOWED_ORIGINS` 和内容为 32 个原始 bytes 的 `TICKET_ENCRYPTION_KEY_FILE`。

## Git 约束

默认只允许修改本地工作树和运行非破坏性验证。未经用户在当前 Goal 中明确授权，不执行 `git add`、commit、remote 配置、push、tag、GitHub 仓库创建、SSH 或部署。
