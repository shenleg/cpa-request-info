# Issue tracker：本地 Markdown

本文件说明本仓库如何使用 `.scratch/` 中的 Markdown 文件管理 issue 和 spec。

## 约定

- 每个 feature 使用独立目录：`.scratch/<feature-slug>/`
- spec 文件路径为 `.scratch/<feature-slug>/spec.md`
- 每个实现 issue 单独存放在 `.scratch/<feature-slug>/issues/<NN>-<slug>.md`，从 `01` 开始编号；不要把多个 issue 合并到一个文件。
- 实现 issue 的 `Status:` 记录进度：`open`（尚未完成）、`in-progress`（实施中）、`completed`（本票范围已实现并通过约定验收）。`completed` 不要求已经 Git 提交或发布，也不代表整个父 spec 完成。
- 未完成 issue 使用独立的 `Triage:` 记录分诊，值见 `triage-labels.md`。`ready-for-human` 只表示需要人工实现，不表示等待验收。
- 兼容旧格式：`Status:` 若仍是五种分诊标签之一，视为 `Status: open` 加对应的 `Triage:`；实际更新该票时再迁移，不批量改其他票。
- 完成时设置 `Status: completed`，移除不再适用的 `Triage:`，勾选有证据的验收项并在 Comments 记录验证结果和未覆盖范围。所依赖的实现票全部 `completed` 后，才解除实现依赖；仅有 `ready-for-agent` 不表示依赖已完成。
- 对话和评论追加到文件末尾的 `## Comments` 标题下。

## 技能要求“发布到 issue tracker”时

在 `.scratch/<feature-slug>/` 下创建文件；必要时先创建目录。

## 技能要求“获取相关 ticket”时

读取用户提供的文件路径或 issue 编号对应的文件。

## Wayfinding 操作

供 `/wayfinder` 使用。一个 map 文件对应多个子 ticket，每个 ticket 使用独立文件。

- **Map**：`.scratch/<effort>/map.md`，正文记录 Notes、Decisions-so-far 和 Fog。
- **子 ticket**：`.scratch/<effort>/issues/NN-<slug>.md`，从 `01` 开始编号，正文描述问题；使用 `Type:` 记录 `research` / `prototype` / `grilling` / `task`，使用 `Status:` 记录 `claimed` / `resolved`。
- **阻塞关系**：在文件顶部附近使用 `Blocked by: NN, NN`。列出的 ticket 全部为 `resolved` 后，该 ticket 才解除阻塞。
- **Frontier**：扫描 `.scratch/<effort>/issues/`，选择未解决、未阻塞且未认领的 ticket；按编号优先选择最小者。
- **认领**：开始工作前，先将 `Status:` 更新为 `claimed` 并保存。
- **解决**：在 `## Answer` 下追加答案，将 `Status:` 更新为 `resolved`，然后把 context 指针（简述和链接）追加到 `map.md` 的 Decisions-so-far。
