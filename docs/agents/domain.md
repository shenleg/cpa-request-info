# Domain 文档

本文件规定工程技能在探索和修改代码库时如何读取领域文档。

## 开始探索前

- 读取根目录的 `CONTEXT.md`。
- 如果根目录存在 `CONTEXT-MAP.md`，根据任务主题读取其中相关的各个 context 文档。
- 读取 `docs/adr/` 中与当前工作相关的 ADR；多 context 仓库还需检查 `src/<context>/docs/adr/` 中的 context 级决策。

上述文件不存在时，静默继续，不要仅因缺少这些文件而提醒或建议立即创建。术语或决策真正明确后，由 `/domain-modeling` 按需创建。

## 文档布局

本仓库采用 single-context 布局：

```text
/
├── CONTEXT.md
├── docs/adr/
└── src/
```

## 使用术语表中的词汇

在 issue 标题、重构提案、假设或测试名称中提及领域概念时，使用 `CONTEXT.md` 定义的术语，不要替换为术语表明确避免的同义词。

如果术语表尚未定义所需概念，应先判断是否引入了项目并不使用的新术语；若确实存在缺口，则记录下来，供 `/domain-modeling` 处理。

## 标记 ADR 冲突

如果输出与现有 ADR 冲突，应明确指出，而不是默默覆盖。例如：

> _这与 ADR-0007（event-sourced orders）冲突，但值得重新讨论，因为……_
