#!/usr/bin/env python3
# 本文件生成仅替换宿主 HTTP/WS 审计接收取时的 Go overlay；解析函数复用测试插件源码。
import json
import pathlib
import sys


def main():
    """按宿主、插件根目录和独立输出目录生成 overlay 路径；结构变化或已有输出时报错。"""
    host, root, output = (pathlib.Path(value).resolve() for value in sys.argv[1:])
    source = host / "sdk/api/handlers/input_audit.go"
    content = source.read_text()
    old = "ReceivedAt: time.Now().UTC()"
    if content.count(old) != 2:
        raise ValueError("宿主 HTTP/WS 接收时钟位置不是预期的两处")
    content = content.replace(old, "ReceivedAt: inputAuditTestNow().UTC()")
    content = content.replace('import (\n', 'import (\n\t"encoding/json"\n\t"fmt"\n\t"os"\n', 1)
    clock = (root / "internal/audit/clock_audittest.go").read_text()
    # 只复制无状态的取时和解析函数，保证宿主与插件读取完全相同的夹具契约。
    now = clock[clock.index("// auditNow "):clock.index("// newMaintenanceTimer ")]
    now = now.replace("auditNow", "inputAuditTestNow")
    reader = clock[clock.index("// readAuditTestClock "):]
    replacement = output / "input_audit.go"
    with replacement.open("x") as stream:
        stream.write(content + "\n" + now + reader)
    overlay = output / "overlay.json"
    with overlay.open("x") as stream:
        json.dump({"Replace": {str(source): str(replacement)}}, stream)
    print(overlay)


if __name__ == "__main__":
    main()
