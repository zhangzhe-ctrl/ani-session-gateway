# AGENTS.md

> 本文件是 Agent 的短路由入口。长期工程约束在 `CLAUDE.md`，动态状态和批次证据在 `docs/execution/`；不要把当前任务流水账写入本文件。

## Required reading

1. 完整读取 `CLAUDE.md`。
2. 完整读取 `docs/design/realtime-session-gateway-p0-v1.1.md`。
3. 读取 `docs/execution/status.md`。
4. 只执行用户明确启动的一个 Goal/Work Package。

## Agent skills

- Module/interface/seam 设计遵循深 Module 原则：外部 interface 小而稳定，Kubernetes/KubeVirt/Redis 细节留在实现内。
- 测试从 Module 的 interface 观察行为，不穿透内部 seam 绑定实现细节。
- 需要改变设计合同、跨仓库契约或部署权限时停止并请求用户决定。

## Stable routes

- 设计合同：`docs/design/realtime-session-gateway-p0-v1.1.md`
- 执行状态：`docs/execution/status.md`
- Goal 模板：`docs/execution/goal-prompts.md`
- 批次证据：`docs/execution/records/`
- Agent 长期约束：`CLAUDE.md`

