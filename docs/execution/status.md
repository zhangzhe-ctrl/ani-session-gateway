# Execution Status

```yaml
project: ANI Realtime Session Gateway P0
design_revision: v1.1
design_status: CLOSED
implementation_status: NOT_STARTED
active_work_package: null
module_path: UNBOUND
remote: UNCONFIGURED
git_authority: LOCAL_WORKTREE_ONLY
deployment_authority: NONE
ani_baseline: 963bc88836c54a1b09cf100b37eb2f2cb2a5a4be
last_updated: 2026-09-02
```

## Preconditions for SG-0

- 用户创建 GitHub 仓库并提供准确 `<MODULE_PATH>`；或者明确批准一个不会再变化的最终 module path。
- 用户用 `/goal` 明确启动 SG-0。
- 工作树没有与 SG-0 冲突的未知改动。

## Human checkpoints

- API submodule 首次发布：用户手工 remote/commit/push/tag。
- ANI-GW-1：用户提供已发布的 `<API_VERSION>` 与 ANI 精确 baseline。
- LIVE-1：用户单独授权 SSH、测试 namespace、fixtures、rollout、删除范围和回滚。

