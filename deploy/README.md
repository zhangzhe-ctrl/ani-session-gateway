# Kubernetes manifest handoff

`base/` 是 SG-4 的静态 Kustomize base，不包含可提交的 Secret 明文，也不会自行创建 namespace。部署前必须由人工完成以下替换和只读检查；本地 Goal 不执行这些命令。

1. 把 `config-map.yaml` 中的 `example.invalid` URL/Origin 替换为隔离环境真实值。HTTPS Console 必须使用 `wss://`。
2. 把 `deployment.yaml` 的 image tag 替换为人工发布的不可变 tag 或 digest。
3. 把 `network-policy.yaml` 的 `10.96.0.1/32` 替换为目标集群 `kubernetes.default.svc` 的实际 ClusterIP `/32`，并确认 DNS、Redis 与 `virt-api` label/port。
4. 查重 NodePort 30081：`kubectl get service -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"/"}{.metadata.name}{" "}{range .spec.ports[*]}{.nodePort}{"\n"}{end}{end}'`。
5. 生成恰好 32 个原始随机 bytes，不要保存 base64 文本、hex 文本或换行：`umask 077; head -c 32 /dev/urandom > ticket.key; ./scripts/check-ticket-key.sh ticket.key`。
6. 人工创建同一个 Secret 中的两个 key：`kubectl -n ani-system create secret generic ani-session-gateway-secrets --from-file=ticket-encryption-key=ticket.key --from-literal=redis-url='<redis URL>'`。Volume mount 会解码 Kubernetes Secret data；进程再次强制检查挂载文件恰好 32 bytes。
7. 渲染与服务端 dry-run：`kubectl kustomize deploy/base`，然后 `kubectl apply --dry-run=server -k deploy/base`。只有另行授权后才能执行；本 Goal 仅运行仓库内静态 gate。
8. 部署前运行 `kubectl auth can-i --as=system:serviceaccount:ani-system:ani-session-gateway`：允许 `get/list pods`、`create pods/exec`、`get virtualmachineinstances.kubevirt.io`、`get virtualmachineinstances/console.subresources.kubevirt.io` 与 `get virtualmachineinstances/vnc.subresources.kubevirt.io`；拒绝 `get secrets`、`delete pods`、`update virtualmachines.kubevirt.io`。

`network_only` gRPC 仅适用于已知风险的隔离测试环境。9090 只有 ClusterIP Service，NetworkPolicy 同时要求调用方 namespace 为 `ani-system` 且 Pod label 为 `app.kubernetes.io/name=ani-gateway`。

