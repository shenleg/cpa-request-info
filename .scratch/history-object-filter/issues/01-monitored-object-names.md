# 历史对象筛选只显示受监控对象名称

Status: completed

## 需求

管理界面的“历史对象筛选”保留“全部对象”，其余选项仅列出当前受监控对象的名称，不显示状态、发现说明或匿名 ID。对象管理面板保持原有详细标签；查询仍使用匿名对象 ID，不按名称匹配。移除当前筛选对象后恢复“全部对象”并重置分页，不删除历史，后端历史查询保持不变。

## 验收

- [x] 下拉选项与当前受监控对象一一对应，文本只有名称，值仍为匿名 ID。
- [x] 初始未监控对象不显示；改名和重新加入监控后选项及时更新，名称按纯文本呈现。
- [x] 移除当前对象后清除对象筛选；已保存历史及后端按对象查询仍可用。
- [x] macOS 和既有隔离 Linux 环境运行快速检查、原生构建、系统与工具测试、相关真实浏览器场景。

## Comments

- 2026-10-17：固定审查基点 `25b78dbf5ca21704719aef91e7c306fc223b1116`；在 `fix/history-object-filter-names` 分支实施，不提交或推送，不部署生产实例。
- 2026-10-17：macOS 与 Linux x86_64 普通用户 `cpa-verify` 对同份源码通过 `make check`、`make build build-retention-test demo`、`go test ./... -count=1`、`make tools-test`；Node 工具测试分别为 4/4 和 7/7。系统包耗时分别为 176.603s、202.551s。
- 2026-10-17：两端分别通过 `npm run browser -- --scenario lifecycle` 与 `npm run browser -- --scenario search-browse`，各场景在同一实例连续两轮，结果均为 `passed`、`ready=true`、`cleanup=true`。已查看两端有限状态截图；对象名称和选项范围由真实页面 DOM 精确断言验证。
- Linux 被测源码及证据保留在 `/opt/cpa-linux-verify.IZCb2S/history-filter-names-clean.Mj77ui/`；本机有限结果为 `.test-data/history-filter-names-evidence-20261017/`。两个修改文件的 SHA-256 在两端一致。已按固定基点检查全部工作区差异和新增票据，`git diff --check` 通过。
- 首轮检查发现 CSP 脚本摘要未同步，修正后重新构建并完成上述复测。本机缺少固定版 Chromium，执行 `make browser-install` 后通过；首个 Linux 快照包含 macOS 附加文件，保留原目录，另建无附加文件快照完成验收。
- 未验证范围：Linux 的 `TestRealENOSPCFailOpen`、`TestRealReadOnlyVolumeFailOpen` 实际跳过（夹具仅支持 macOS），不计入通过；本票不重跑容器和生产部署。筛选改动不涉及存储或宿主契约。无人工验收待办，未提交、推送或部署。
