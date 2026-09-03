# SG-CONNECTED-SESSION-DEPLOY-20260903

## 授权与范围

- 用户明确授权：合并本仓库未合并 PR、提交并合入本次修改、发布最新镜像并部署到既有测试环境。
- GitHub 在执行时没有 open PR，因此 PR 合并阶段没有可执行对象，也没有创建空合并。
- 部署目标沿用既有隔离测试环境：namespace `ani-system`，控制面 `10.10.1.66:6443`，WebSocket NodePort `30082`。

## Git 发布

- 架构实现提交：`58393cd` (`refactor: deepen connected session lifecycle`)
- `main` 合并提交：`d0353b80383b` (`merge: connected session lifecycle and live rollout records`)
- `origin/main`：已从 `d86a40d` 推进到 `d0353b8`。

## 镜像

- Published image: `docker.changqingyun.cn/ani/ani-session-gateway:d0353b80383b`
- Registry and running image digest: `sha256:552101eaa1cb6e1b8d7ad0670451b32f896ec4a39260d19092b0e3bed34ad5cd`
- 构建时曾生成一个仅存在于本机且 SHA 字符有误的 tag `d0353b8eeb42`；发现于 push 前。同一镜像被重新标记为正确 tag，错误 tag 未推送、未部署。

## 部署与基础验证

- `make manifests diff-check`: passed.
- `kubectl apply --dry-run=server -k ...`: passed；仅 Deployment image 发生配置变化。
- `kubectl apply -k ...`: passed.
- `kubectl rollout status deployment/ani-session-gateway --timeout=180s`: successfully rolled out.
- Deployment generation/observedGeneration: `6/6`; updated `1`, ready `1`, unavailable `0`。
- Pod: `ani-session-gateway-7bd7d8dc77-2vnwr`; Ready `true`; restart count `0`。
- `/healthz`: `ok`。
- `/readyz`: `ready mode=redis degraded=false`。
- WebSocket and gRPC endpoints both resolve to the new Pod; ANI Gateway Pod can connect to the gRPC Service on `9090`。

## 新架构冒烟

没有创建或删除业务 fixture。一次性静态客户端完成：

```text
gRPC CreateSession
-> one-time ticket claim
-> WebSocket ani.terminal.v1 upgrade
-> nonexistent VMI runtime open
-> sanitized RUNTIME_UNAVAILABLE frame
-> WebSocket 1011 close
```

Result:

```text
smoke=pass error_code=RUNTIME_UNAVAILABLE close_status=1011
ani_session_end_total{mode="serial",outcome="runtime_unavailable"} 1
ani_session_runtime_errors_total{code="open_failed",mode="serial"} 1
```

日志只包含安全事实和 bounded outcome，没有 ticket、connect URL、payload 或底层 runtime error。runtime 未打开，因此 active gauge 没有错误递减。

## 权限复验

- Allow: pods get/list, pods exec create（使用 `--subresource=exec` 验证）, VMI get, console get, VNC get。
- Deny: Secret get, Pod delete。

## 未验证与回滚点

- 部署时没有现存 VMI 或带 ANI workload 标签的业务 Pod，因此未重新执行真实 Pod exec、serial 字节流、VNC RFB/render/input。
- 未执行 ticket 过期/重放/跨租户、NetworkPolicy 非匹配 Pod 负向隔离、Redis 中断或 restart persistence。
- 结论为 `DEPLOYED_SMOKE_VERIFIED`，不是完整 `LIVE-1 passed` 或 production-ready。
- 直接镜像回滚点：`docker.changqingyun.cn/ani/ani-session-gateway:d86a40d33369`，digest `sha256:18dba07f6ef85e0466f72048a1c73474b9664f40c792e3eefce95838d409bb3d`。
