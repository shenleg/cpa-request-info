# A 版请求监控工作台

## Context
原输入审计功能可用，但页面缺少层级、布局和统一样式。用户体验单 HTML 模拟原型后明确选择 A · CPAMP 工作台，正式实现只采用 A 的布局和交互，保留原接口及审计语义。

## 已确认方案
- 页头操作、重要状态、双行筛选、四项真实摘要、紧凑记录表和右侧详情抽屉。
- 对象管理、存储设置、诊断使用次级面板，主界面优先浏览记录。
- 保留纯文本展示、鉴权撤销、独立错误、缺口语义、设置确认和状态轮询不覆盖草稿。
- 不添加调用成功率、Token、费用或虚构趋势，不引入前端框架或外部资源。
- 用户随后授权提交并合并。一次性原型归档在 `archive/cpamp-ui-prototype` 分支的 `.scratch/cpamp-style-audit-ui/request-monitor.prototype.html`；本地副本保留并忽略，不进入主分支。正式页面只保留已选 A 的实现。

## 实现票
- [01 · 正式页面与验收](issues/01-production-monitor-ui.md)
- [02 · 取消已不需要的 pre-commit 安装功能](issues/02-remove-pre-commit.md)
