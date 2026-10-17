#!/usr/bin/env python3
"""在 Linux 固定官方容器中验收本轮正式插件；只用合成输入，停容器但保留数据和结果。"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request


class Upstream(http.server.BaseHTTPRequestHandler):
    """提供合成模型回复，不记录请求正文或认证材料。"""

    def do_POST(self):
        """读取本轮请求并返回固定成功回复；异常由测试失败直接报告。"""
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if request.get("stream"):
            payload = 'data: {"id":"synthetic-sqlite","object":"chat.completion.chunk","model":"audit-test-model","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}\n\ndata: [DONE]\n\n'
            media = "text/event-stream"
        else:
            payload = json.dumps({"id": "synthetic-sqlite", "object": "chat.completion", "model": "audit-test-model", "choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}]})
            media = "application/json"
        self.send_response(200)
        self.send_header("Content-Type", media)
        self.end_headers()
        self.wfile.write(payload.encode())

    def log_message(self, *_args):
        """禁用合成上游访问日志，避免复制输入或请求路径。"""


def request(base, method, path, key=None, body=None):
    """发送本轮本地 HTTP 请求，返回状态与 JSON；HTTP错误仍返回状态，其余异常抛出。"""
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Authorization"] = "Bearer " + key
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=5) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def eventually(check, timeout=30):
    """等待本轮明确成功条件并返回结果；到期抛出，不把无结果当作成功。"""
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            value = check()
            if value:
                return value
        except (ConnectionError, urllib.error.URLError):
            pass
        time.sleep(0.1)
    raise RuntimeError("本轮容器条件等待超时")


def main():
    """创建独立合成容器并验证新目录、保存、鉴权及重启；失败仍停止本轮容器，不删除现场。"""
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    output = args.output.resolve()
    if output.exists():
        raise RuntimeError("结果目录必须是新的目录")
    output.mkdir(mode=0o700)
    (output / "plugins").mkdir(mode=0o700)
    library = args.source.resolve() / ".build/plugins/request-input-audit.so"
    copied = output / "plugins/request-input-audit.so"
    copied.write_bytes(library.read_bytes())
    copied.chmod(0o600)
    os.chown(output, 1000, 1000)
    os.chown(output / "plugins", 1000, 1000)
    os.chown(copied, 1000, 1000)
    upstream = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    admin = "synthetic-sqlite-container-admin-only"
    client = "synthetic-sqlite-container-client-only"
    config = f'''config-version: 8
server:
  host: 127.0.0.1
  port: {port}
management:
  secret-key: "{admin}"
  disable-control-panel: true
  disable-auto-update-panel: true
access:
  api-keys: ["{client}"]
auth-dir: /CLIProxyAPI/sqlite-test/auth
logging-to-file: false
request-log: false
plugins:
  enabled: true
  dir: /CLIProxyAPI/sqlite-test/plugins
  configs:
    request-input-audit:
      enabled: true
      state-dir: /CLIProxyAPI/sqlite-test/plugins/state
      budget-bytes: 16777216
openai-compatibility:
  - name: controlled
    base-url: http://127.0.0.1:{upstream.server_port}/v1
    api-key-entries:
      - api-key: synthetic-container-upstream-only
    models:
      - name: audit-test-model
        alias: audit-test-model
'''
    config_path = output / "config.yaml"
    config_path.write_text(config)
    config_path.chmod(0o600)
    os.chown(config_path, 1000, 1000)
    name = "cpa-sqlite-" + str(time.time_ns())
    image = "eceasy/cli-proxy-api:v8.0.27"
    api = "/v0/management/plugins/request-input-audit"
    base = f"http://127.0.0.1:{port}"
    started = False
    try:
        subprocess.run(["docker", "run", "-d", "--name", name, "--network", "host", "--user", "1000:1000", "-v", f"{output}:/CLIProxyAPI/sqlite-test", image, "./CLIProxyAPI", "--config", "/CLIProxyAPI/sqlite-test/config.yaml", "--local-model"], check=True, stdout=subprocess.DEVNULL)
        started = True
        def available():
            status, value = request(base, "GET", api + "/status", admin)
            return value if status == 200 and value.get("available") and value.get("state", {}).get("state") == "active" else None
        eventually(available)
        code, _ = request(base, "POST", "/v1/chat/completions", client, {"model": "audit-test-model", "messages": [{"role": "user", "content": "synthetic-discover-only"}]})
        assert code == 200, "发现请求失败"
        code, page = request(base, "POST", api + "/records/search", admin, {})
        assert code == 200 and page["total"] == 0, "未监控时保存了正文"
        code, objects = request(base, "GET", api + "/objects", admin)
        assert code == 200 and len(objects["objects"]) == 1, "未发现对象"
        object_id = objects["objects"][0]["id"]
        code, _ = request(base, "POST", api + "/monitor", admin, {"object_id": object_id, "name": "SQLite合成容器"})
        assert code == 200, "启用监控失败"
        marker = "SQLITE-CONTAINER-" + str(time.time_ns())
        code, _ = request(base, "POST", "/v1/chat/completions", client, {"model": "audit-test-model", "messages": [{"role": "user", "content": marker}]})
        assert code == 200, "本轮模型请求失败"
        def saved():
            code, page = request(base, "POST", api + "/records/search", admin, {"q": marker})
            return page if code == 200 and page["total"] == 1 else None
        page = eventually(saved)
        record = page["records"][0]
        code, detail = request(base, "GET", api + "/detail?id=" + record["id"], admin)
        assert code == 200 and detail["text"] == marker, "详情不符合本轮正文"
        for key in (None, client):
            code, _ = request(base, "POST", api + "/records/search", key, {})
            assert code in (401, 403), "管理记录可被非管理凭据读取"
        subprocess.run(["docker", "restart", "-t", "10", name], check=True, stdout=subprocess.DEVNULL)
        eventually(available)
        restored = eventually(saved)
        assert restored["records"][0]["id"] == record["id"], "重启未恢复本轮记录"
        state = output / "plugins/state"
        assert (state / "records.sqlite").read_bytes().startswith(b"SQLite format 3\x00"), "主存储不是SQLite"
        assert not list(state.glob("records-*.jsonl")), "仍写入JSONL"
        assert (state / "caller-scope-v1-sqlite-v1.seed").exists(), "新存储身份缺失"
        image_id = subprocess.check_output(["docker", "inspect", "--format", "{{.Image}}", name], text=True).strip()
        result = {"passed": True, "container": name, "image": image, "image_id": image_id, "uid_gid": "1000:1000", "platform": "linux/amd64", "plugin_sha256": hashlib.sha256(copied.read_bytes()).hexdigest(), "checks": ["native-load", "default-unmonitored", "discover-monitor-save", "management-auth", "search-detail", "sqlite-files", "restart-recovery"]}
        (output / "result.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
        print(json.dumps(result, ensure_ascii=False))
    finally:
        if started:
            subprocess.run(["docker", "stop", "-t", "10", name], check=True, stdout=subprocess.DEVNULL)
        upstream.shutdown()
        upstream.server_close()


if __name__ == "__main__":
    main()
