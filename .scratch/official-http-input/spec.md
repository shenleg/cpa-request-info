# 原版 CPA HTTP 执行输入记录精简版

Status: completed

## 目标

在当前 experiment/official-plugin-probe 分支将正式插件改为官方执行拦截接口，不修改或重编用户正在运行的 CPA。保留三协议 HTTP/SSE 末尾允许输入记录、对象命名、查询、预算、保留期与采集故障诊断。

## 新契约

- 一次记录对应官方 Before 所见的一次外部 HTTP 模型执行，而不是任意已认证入站请求。时间为回调观察时刻，模型可能已经由 handler 改写。
- 仅 Chat Completions、Responses HTTP、Anthropic Messages；WebSocket 明确不采集。通过官方 source 标记排除插件内部嵌套执行，不按 TraceID 或正文猜测去重。
- 只发现实际到达回调的匿名对象；默认不监控。首次请求不保存正文，管理员选择并命名后只采集后续请求。未选择对象仅在有界内存目录中，重启后重新发现；已选择对象持久恢复。
- caller_scope 只在回调内用于带私有种子的域分隔 HMAC，不持久保存原 scope、Key 或请求头。第一版只声明内置客户端 Key 配置已验收。
- 保留原正文白名单和工具/历史/系统排除规则。不记录完整请求、上游回复或原附件载荷。
- 列表和搜索使用 POST JSON，关键词不得进入原版 URL 访问日志。
- 新身份格式使用全新 state-dir。旧 identity.seed/data 目录拒绝混用，不删除、迁移或覆盖旧数据。
- 未进入回调的路由前失败/宿主回调缺失无法计数；状态如实显示范围，不以零故障代表完整覆盖。native 仍同进程，不提供崩溃隔离。

## 非目标

不保留补丁/原版双运行模式，不实现前置代理，不支持 WebSocket 原帧审计，不承诺任意官方未来版本或第三方认证来源，不操作生产实例。

## 历史与取舍

原完整入站审计 spec 和验收保留为历史，本需求经用户明确批准改变范围。实测依据见 docs/verification/official-plugin-probe-2026-10-16.md；新的取舍见 docs/adr/0004-official-http-execution.md。

## 实施

[01-official-http-input](issues/01-official-http-input.md)。2026-10-16 完成本轮实现及 macOS arm64、Linux amd64 同源码默认验收、官方容器加载与真实磁盘故障专项；详见[交付记录](../../docs/verification/official-http-input-2026-10-16.md)。尚未提交、推送或生产部署，Linux arm64 精简版未验证。
