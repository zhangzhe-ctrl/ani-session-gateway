# ANI Session Gateway

ANI 的实时会话执行进程，P0 覆盖 Kubernetes Pod terminal、KubeVirt serial console 与 KubeVirt VNC/noVNC。

## 当前状态

```text
设计：CLOSED（v1.1）
实现：NOT_STARTED
当前 Work Package：无
GitHub remote：未配置，由用户手工创建和提交
Go module path：未绑定，SG-0 启动前由用户提供
测试环境部署：未授权
```

## 权威读取顺序

1. `AGENTS.md`
2. `CLAUDE.md`
3. `docs/design/realtime-session-gateway-p0-v1.1.md`
4. `docs/execution/status.md`
5. 当前 Goal 对应的 `docs/execution/records/<ID>.md`

## 实施批次

```text
SG-0  仓库骨架、API submodule、双 listener
SG-1  SessionManager、Memory/Redis Store
SG-2  WebSocket transport、Pod exec
SG-3  KubeVirt serial、VNC
SG-4  Kubernetes 部署、RBAC、NetworkPolicy

ANI-GW-1  在 ANI 仓库接入内部 gRPC
CONSOLE-1 前端 terminal/serial/noVNC
LIVE-1    隔离测试环境产品链路门禁
```

可直接使用的 Goal 提示词见 `docs/execution/goal-prompts.md`。

## Git 约束

默认只允许修改本地工作树和运行非破坏性验证。未经用户在当前 Goal 中明确授权，不执行 `git add`、commit、remote 配置、push、tag、GitHub 仓库创建、SSH 或部署。

