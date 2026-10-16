# 将正式插件改为原版 CPA HTTP 执行输入记录

Status: completed

## 范围

用户要求在当前 experiment/official-plugin-probe 分支实施。保留此前未提交探针。Git 审查基点 6711c1cb7a08c432e8690e66c87a3a74ad752480；原探针文件摘要见 .test-data/official-http-baseline._gtl_pg3/baseline.json。

## 验收

- [x] 正式插件只使用官方 Before/管理契约，构建不应用宿主补丁，可独立构建动态库。
- [x] HTTP/SSE、身份发现/命名/重启、重试、同文重交、失败/取消；排除 nested 与 WS。
- [x] 新身份种子持久且计入预算，旧目录拒绝混写，明文 Key/scope 不保存。
- [x] POST 查询、管理鉴权、纯文本展示、保留期/预算/诊断回归。
- [x] 旧契约用例逐项说明替代覆盖，不批量 skip 冒充兼容；历史文件保留。
- [x] macOS 与隔离 Linux 同源码快速检查、正式原生构建、系统/工具/浏览器验收。
- [x] 固定官方镜像内非 root 插件加载、采集和重启恢复；真实存储故障专项。
- [x] README/安装/ADR/CONTEXT 更新，记录有限证据及未验证项。

## Comments

- 2026-10-16：用户批准方案并开始实施；不新建分支、不提交/推送/合并/部署，不删除文件或旧数据，不修改真实凭据、.env 或 CI/CD。
- 2026-10-16：macOS arm64、Linux amd64 同源码 `make verify` 与独立官方探针通过，106 个入口摘要一致；两端浏览器各16/16。官方 v8.0.27 镜像非 root 挂载正式库、发现不存正文、两条采集/管理保护/重启保持通过。Linux 私有64MiB tmpfs实际ENOSPC/EROFS专项通过；macOS实际hdiutil故障由默认系统套件运行。核心工作区审查没有已确认实质性缺陷，diff检查通过。详见[交付记录](../../../docs/verification/official-http-input-2026-10-16.md)。Linux arm64、生产负载与未来宿主版本未验证；未提交/推送/生产部署。
