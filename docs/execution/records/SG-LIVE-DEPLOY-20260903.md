# SG-LIVE-DEPLOY-20260903

## 范围与授权

- 用户明确授权把 `ani-session-gateway` 与已接入 Session Gateway 的 `ani-gateway` 部署到现有测试环境 `ani-system`。
- 用户指定 Session Gateway WebSocket NodePort 为 `30082`，镜像仓库前缀为 `docker.changqingyun.cn/ani/`。
- 初始部署授权不包含产品 fixture；后续用户单独授权创建一台 VM、修正目标镜像用途标签并补齐 ANI Gateway 的最小 KubeVirt RBAC。仍未授权故障注入、删除资源或声明完整 LIVE-1 通过。

## 发布物

- `docker.changqingyun.cn/ani/ani-session-gateway:d86a40d33369`
  - registry digest: `sha256:18dba07f6ef85e0466f72048a1c73474b9664f40c792e3eefce95838d409bb3d`
- `docker.changqingyun.cn/ani/ani-gateway:dev-20260903-session-gateway`
  - registry digest: `sha256:7ac473df8b36d090ba2eeef7769487f7c7ced5d46f1785a19efccddc7ccfa59f`

## 环境配置

- namespace: `ani-system`
- Gateway API: `http://10.10.1.66:30080`
- Console Origin: `http://10.10.1.66:30081`
- Frontend local-development Origin: `http://localhost:5173`（仅供前端本地开发，不是生产来源）
- Session WebSocket: `ws://10.10.1.66:30082/api/v1/realtime`
- internal gRPC: `ani-session-gateway-grpc.ani-system.svc.cluster.local:9090`
- store: Redis，复用现有集群 Redis Service；Secret 内容未写入仓库或证据。

## 部署时修复

1. `30081` 已被 `ani-console` 占用，按用户决定改为 `30082`，部署前复核无冲突。
2. distroless 镜像声明字符串用户时，Kubernetes 无法验证 `runAsNonRoot`；清单显式固定 `runAsUser/runAsGroup: 65532`。
3. Secret volume 原 `0400` 只能由 root 读取；改为 `0440`，并固定 `fsGroup: 65532` 与 `OnRootMismatch`，只授予应用组读取。
4. NetworkPolicy Redis egress selector 改为目标集群真实 label `app=ani-reconcile-ha-redis`。
5. 首次真实 Pod exec 发现 Session Gateway 在 Pod resolver 阶段等待约 30 秒后返回 `runtime_open_failed`。同一 ServiceAccount 通过控制面直接 `kubectl exec` 成功，目标 Pod Running/Ready 且双 label、容器名正确；集群 Service VIP 为 `10.96.0.1:443`，实际 API endpoint 为 `10.10.1.66:6443`。NetworkPolicy 因此补充实际 endpoint 的 `/32:6443` egress，同时保留 Service VIP 规则。

## 已验证

- `kubectl apply --dry-run=server -k ...` 通过。
- `ani-session-gateway` Deployment `1/1 Ready`；Pod 无重启。
- `ani-gateway` Deployment `1/1 Ready`，新镜像与 `SESSION_GATEWAY_GRPC_ADDR`/`SESSION_GATEWAY_GRPC_TIMEOUT` 生效。
- `http://10.10.1.66:30082/healthz` 返回 `ok`。
- `http://10.10.1.66:30082/readyz` 返回 `ready mode=redis degraded=false`。
- Gateway Pod 到 Session Gateway ClusterIP `9090` TCP 连通。
- ServiceAccount 允许 pods get/list、pods exec create、VMI get、console/vnc subresource get；拒绝 Secret get、Pod delete、VM update。
- 未认证 exec API 返回 HTTP `401`。
- `go test ./internal/deployment` 在隔离 Docker 容器中通过。
- ANI Gateway `go test ./...` 在隔离 Docker 容器中通过。
- NetworkPolicy API endpoint 修复经清单契约测试、server dry-run、apply 后回读验证通过。
- `ALLOWED_ORIGINS` 已配置为 `http://10.10.1.66:30081,http://localhost:5173`；ConfigMap annotation `ani.kubercloud.io/localhost-origin-purpose=frontend-local-development-only`、Deployment `envFrom` 引用、`1/1 Ready` 与 WebSocket NodePort `30082` 已回读验证。
- 用户复验真实 Pod exec 已成功。
- 通过 ANI Gateway 正式 `POST /api/v1/instances` 创建 KubeVirt VM `inst_138fc7cc-3770-4436-be5a-83f127561302`；Gateway 列表/详情与 KubeVirt VM/VMI 最终均为 `running`/Ready。
- VM serial 以 `ani.terminal.v1` 完成 REST -> gRPC -> one-time ticket -> WebSocket -> KubeVirt 数据面验证；合法 JSON stdin frame 连续收到 stdout。
- VM VNC 以 `ani.vnc.v1` 完成 HTTP 101 upgrade，并收到 binary RFB `RFB ` 前缀握手；错误使用 `ani.terminal.v1` 时稳定返回 400 `session subprotocol does not match mode`。
- 诊断确认 serial 发送裸文本 `ls` 会稳定得到误导性的 `RUNTIME_STREAM_FAILED`；协议要求发送 JSON text frame，例如 `{"type":"stdin","data":"ls\\r"}`。该错误分类缺口未在本批修改。

## 未验证

- guest serial 登录和命令执行语义（本批只验证双向字节流与 stdout）。
- VNC 图像渲染、键盘和鼠标输入（本批只验证 101 与 binary RFB 握手）。
- ticket 重放、过期、跨租户和无 scope 负向路径。
- NetworkPolicy 非匹配 Pod 访问 gRPC 的负向实测。
- Redis 中断、Pod restart 后的持久与恢复语义。

因此本批结论是 `DEPLOYED_PARTIAL_E2E_VERIFIED`，不是完整 `LIVE-1 passed` 或 production ready。

## 回滚点

- ANI Gateway 前一镜像：`docker.changqingyun.cn/ani/ani-gateway:dev-20260902-resize-ns`。
- Session Gateway 是本批新增资源；回滚时应先恢复 Gateway 镜像/移除 Session Gateway 地址，再停止 Session Gateway 外部入口和 Deployment。删除动作需再次获得用户授权。

## 后续覆盖与恢复

- 初次部署后，`ani-gateway` 曾被外部 rollout 覆盖为 `dev-20260903-rtfix`；该镜像不包含 Session Gateway gRPC issuer，导致 exec API 返回旧的内部占位 URL。
- 用户明确要求恢复后，Gateway 已重新切换为 `dev-20260903-session-gateway`。
- 恢复后实际 Pod image digest 为 `sha256:7ac473df8b36d090ba2eeef7769487f7c7ced5d46f1785a19efccddc7ccfa59f`，Deployment `1/1 Ready`、Pod 零重启、HTTP health/ready 与内部 gRPC TCP 再次通过。
- 覆盖期间签发的旧 URL 不可复用；客户端必须使用新的幂等键重新创建 session。

## 2026-09-03 前端开发 Origin 变更说明

- 首次单文件 apply 未显式指定 namespace，因清单依赖 kustomize 注入 namespace，临时在 `default` 创建了同名 ConfigMap；发现后立即核对并删除，该 namespace 未创建或重启 Session Gateway Deployment。
- 随后使用显式 `-n ani-system` 应用并滚动重启；最终核验 `default` 中无该 ConfigMap，`ani-system` 中配置与用途 annotation 均正确。
