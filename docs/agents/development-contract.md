# Agent Development Contract

## 责任

Agent 负责在用户明确启动的一个 Work Package 内完成本地实现、测试、验证和证据记录。Agent 不负责替用户创建 GitHub 仓库、决定 module path、批准安全风险或隐式获得测试/生产环境权限。

## 增长控制

- `AGENTS.md` 只做短路由。
- `CLAUDE.md` 只放稳定约束。
- 设计决策只进入版本化设计合同或 ADR。
- 当前状态只进入 `docs/execution/status.md`。
- 单批次过程与证据只进入 `docs/execution/records/<ID>.md`。

## 冻结技术基线

权威选择见 `docs/design/realtime-session-gateway-p0-v1.2.md`。Agent 必须使用 `net/http + go-chi/chi/v5`、`coder/websocket`、`grpc-go`、`client-go/tools/remotecommand`、`kubevirt.io/client-go`、Redis、Prometheus Go Client、OpenTelemetry Go 与环境变量启动强校验。不得静默替换为同类库、标准库路由或自研 framework。Redis 是唯一生产/部署 Session Store；MemoryStore 只允许测试/显式本地开发，禁止自动降级。

## 停止条件

遇到以下任一情况必须停止当前状态变更并报告：module path 未绑定、设计合同冲突、跨出 allow paths、需要 remote/push/tag、需要 SSH/部署、真实依赖缺失、需要破坏性动作或需要把测试环境结论外推为生产结论。
