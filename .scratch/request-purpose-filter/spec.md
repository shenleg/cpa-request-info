# 客户端请求用途过滤与记录标识

Status: completed

## 目标

优先使用已有请求头和专用路径排除客户端辅助调用，只改变采集副本，不拒绝或改写模型请求。不要求客户配置变化，不承诺恢复客户端加工前的人工原文。无用途标记仍按现有规则提取，保存的记录明确标为用途未知。

## 契约

- 首轮仅读取 Headers 和 request_path，不解析 body.client_metadata 用途，不根据 UA、session/prompt ID 或 context-compacted 推断人工来源。
- 两条 Responses compact 路径及有效非空 Claude agent-id / Codex subagent 表示明确排除；保留匿名对象发现，不存正文、不增加 gap。
- Claude request-class 的 subagent/workflow/compaction/auxiliary、Codex request_kind 的 prewarm/compaction/memory 正常跳过；包括用户主动委派任务产生的合成执行输入。
- Claude main 保存 purpose=main；Codex turn 在无子任务标记时保存 purpose=turn；无标记、未知、损坏或互斥信号保存 purpose=unknown。有效子任务证据优先，turn+subagent 不是冲突。main+turn 可保留 main；对话与压缩用途互斥则未知。
- 头名忽略大小写；重复相同值可接受，冲突值不依赖 map 顺序选择。可选用途字段损坏不产生采集故障，不放宽原必需回调检查。
- 明确 Claude main 绕过整条辅助模板判定，unknown 沿用旧辅助模板规则；保留请求均继续原有正文白名单、工具排除、提醒/skill/IDE 清理和截断。
- 新记录持久用途仅 main/turn/unknown；旧记录缺省仍为空，展示未记录用途，不改写历史。列表和详情标注主会话调用、对话轮次、用途未知或未记录用途；不是人工认证。
- 新插件读取旧记录；旧插件严格拒绝新字段，不支持直接复用升级后的 state 降级。队列/磁盘预算计入新字段，不加迁移或 sidecar。
- 不改变首次发现、未监控不保存、暂停、容量、官方内部 source 与 WebSocket 排除等契约；不保存原始用途头、子代理 ID 或认证值。

## 非目标

不新增关键词封禁、规则配置系统、用途筛选或新表列；不修改客户配置，不自动开启 hint，不操作生产、删除历史、提交或推送。

## 实施

[01-request-purpose-filter](issues/01-request-purpose-filter.md)。按批准方案实施，基点和实际验收在该票及独立报告记录。
