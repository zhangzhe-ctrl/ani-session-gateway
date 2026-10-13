# Resource 实例终端接入

该可选接入复用现有 SessionService、票据持久化、WebSocket 和 Kubernetes exec。当前任务由 Resource 的 `docs/execution/status.md` 维护唯一结果；本文是配置/协议说明，不代表环境部署或 GPU 终端验收。

Gov 的可信目标增加 `Target.instance_binding`，包含保存的逻辑 cluster、Namespace、Notebook 名称/UID、Pod 名称/UID、容器、shell 和实例业务版本。SG 把它纳入幂等指纹、持久 Session 和 Claim；改换 Pod UID 不能重放同一票据。旧目标未启用 binding 时继续遵循已有合同。

启用后必须配置：

| 配置 | 来源 |
| --- | --- |
| `RESOURCE_INSTANCE_ADDR` | Resource 同进程终端验证 listener |
| `RESOURCE_INSTANCE_SERVER_NAME` | 该服务证书的精确 DNS SAN |
| `RESOURCE_INSTANCE_CA_FILE/CERT_FILE/KEY_FILE` | 受管信任链与 SG 客户端身份，唯一 URI `spiffe://ani.internal/service/ani-session-gateway` |
| `RESOURCE_INSTANCE_CLUSTER_ID` | 实际 exec 所连集群对应的逻辑 cluster UUID，必须等于冻结目标 |
| `INSTANCE_CONTROL_CA_FILE/CERT_FILE/KEY_FILE` | 控制口受管 TLS 1.3 配置，启用实例时无明文回退 |

只有经过 mTLS 验证且唯一 URI 为 `spiffe://ani.internal/service/ani-governance` 的直接客户端才能签发受管实例票据；普通可信 CA、请求 header 或自报 tenant 均不能替代该身份。Resource 的窄 API 模块 `github.com/zhangzhe-ctrl/ani-resource-service/api/instance/terminal` 只含 protobuf/gRPC；SG 不导入 Resource 根模块的 Kubernetes SDK。固定模块版本以 `go.mod` 为准；升级须先发布 provider，再校验来源 SHA 与 sums，并以 `GOWORK=off` 验证消费者。

签发和建连均调用 `InstanceTerminalService/ValidateTerminalTarget`。停止/删除意图、新版本、Notebook/Pod UID 或容器不匹配时拒绝；Redis 失败不降级为内存权限。PodResolver 只使用保存 Namespace 与精确 Pod UID，不自动转接同名替换 Pod。exec 再以容器内 downward API 的 `ANI_INSTANCE_POD_UID` 校验实际 UID，覆盖查询后发生替换的窗口。

票据密钥、Redis、Origin/WS 与 Kubernetes 身份继续沿用现有受管配置。重启使用相同票据密钥和持久 store；恢复控制端不清除冻结 binding 或改写旧票据。此接入只约束新连接，长连接会话生命周期仍由已有 SG 机制承担。前端由专人接手，使用 Governance 的实例 exec API 返回的短期 `ws_url`，不要将内部 URI/mTLS 或运行位置交给页面。
