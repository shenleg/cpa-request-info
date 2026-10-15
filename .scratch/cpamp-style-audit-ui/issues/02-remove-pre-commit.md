# 取消仓库 pre-commit 安装功能

Status: completed
Review base: b5e3e494220eb68207792b89ca68402bf3c5a995

## Context
用户明确说明 `.githooks/pre-commit` 是手动删除且不再需要，随后明确授权清理安装入口、安装脚本、专属测试和说明，并取消本仓库 local `core.hooksPath`。这属于取消功能，不是恢复丢失文件或把失败测试改成跳过。

## 范围
- 移除 Makefile 的 hooks-install 和 scripts/install-hooks.sh；保留用户原有 hook 删除。
- 删除六个 TestInstall* 及专用 copyPublicTools，保留 Fast/Staged/overlay 测试。
- 保留手动 make check / make check-staged，更新当前使用文档和过时注释。
- 取消本仓库 local core.hooksPath，不改全局 Git 配置。
- 不改 A 版页面、原型、业务接口或后端，不提交、推送、合并或部署。

## 验收
- [x] 安装入口、脚本、专属测试及配置已移除；手动检查保留。
- [x] 当前说明准确，历史失败记录保留并追加后续结果。
- [x] macOS 最新源码快速检查、构建、Go、工具和16场浏览器通过。
- [x] Linux 同源码对应验收通过；平台skip如实记录。
- [x] 本次增量审查完成，旧 UI 实现无变更。

## Comments

- 2026-10-16：用户通过选择题明确授权上述清理及脚本删除；旧 `.githooks/pre-commit` 不恢复。此前三项工具失败为历史事实，新结果另行记录。
- 最新r4共85项执行源码，清单摘要 `5d00108e31a784a8356075b1cb9532890448feba976c108dfa20663e4bfdfba6`；macOS `make verify` 全通过，Linux所有验收阶段exit0，Go365通过/0失败/2 hdiutil平台skip。两端浏览器各16/16、Node工具7/7、磁盘CLI工具4/4；手动检查有效测试仍全部通过，不存在跳过安装测试掩盖失败的情况。
- Standards 0项、Spec 0项。两端85项源码一致且验收前后未变，Linux普通测试用户/挂载无遗留。有限证据与完整说明见 `docs/verification/cpamp-style-audit-ui-2026-10-16.md` 后续节；设为completed，未提交、推送、合并或部署，无人工待办。
