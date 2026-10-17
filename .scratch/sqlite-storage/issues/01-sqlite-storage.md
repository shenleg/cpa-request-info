# 01 — 将记录主存储改为 SQLite

Status: completed

## 背景

用户要求替换 JSONL，明确放弃历史数据并在完成后提供安装说明。规范见 ../spec.md。

## 验收项

- [x] 内嵌 SQLite 记录提交、索引查询及元数据语义。
- [x] 新目录和双向格式拒绝，无迁移、无删除旧历史。
- [x] 全目录预算、持久日志预留、满额/降额/清理/恢复及故障分类。
- [x] 适配 JSONL 绑定的单元、系统、工具和浏览器夹具。
- [x] 最新源码 Linux 验收及适用容器、磁盘专项（用户明确本机不参与）。
- [x] 工作区 Standards/Spec 审查和受影响复测。
- [x] 正式产物、SHA-256、新目录安装和回滚说明。

## Comments

- 2026-10-17：实施开始，固定审查基点 `791c807616b65ec706ace55fd98e6788f26d78cd`，分支 `feat/sqlite-storage`。不自动提交、推送、生产部署或删除。
- 2026-10-18：完成本票全部已授权 Linux 验收：快速检查、固定官方构建、全量Go系统/单元/工具、audittest、race、16项浏览器与2项额外预算回归、固定官方容器、64MiB私有tmpfs真实ENOSPC/只读；双轴审查各0项，修复后限定复核完成。用户明确本机不参与，macOS未执行，arm64/musl未验证，不冒称通过。
- 正式库及安装说明：`.build/releases/sqlite-storage-0.3.0-linux-amd64.20261017/`。SHA-256 `ea153fbd0119189719a1658f2893c2213966c2a61fd4b8020e8b04acecce84bf`。详情见 `docs/verification/sqlite-storage-2026-10-17.md`；旧目录未删除、无迁移、未生产安装、未提交或推送。
