#!/usr/bin/env python3
"""以固定官方镜像、非 root 身份和合成上游验证动态库；只停止容器，保留容器、数据及有限证据。"""

import argparse
import hashlib
import http.server
import json
import os
import pathlib
import socket
import subprocess
import threading
import time
import urllib.error
import urllib.request

ADMIN = 'synthetic-container-admin-only'
CLIENT = 'synthetic-container-client-only'
API = '/v0/management/plugins/request-input-audit'


class Upstream(http.server.BaseHTTPRequestHandler):
    """仅返回本地合成模型响应，不保存或输出请求、认证头和回复原文。"""

    def do_POST(self):
        """消费本次请求并返回固定成功 JSON；网络异常交由测试失败处理。"""
        self.rfile.read(int(self.headers['Content-Length']))
        raw = json.dumps({'id': 'synthetic-container-reply', 'object': 'chat.completion', 'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'synthetic reply'}, 'finish_reason': 'stop'}]}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_):
        """不输出合成请求日志，有限结果由主测试生成。"""


def docker(*args):
    """执行明确的 Docker 操作并返回输出；命令失败直接抛错，不吞掉错误。"""
    return subprocess.check_output(['docker', *args], text=True).strip()


def request(base, path, method='GET', body=None, key=ADMIN):
    """调用测试端点，返回状态、借用响应正文和头；HTTP 拒绝正常返回，网络错误抛出。"""
    raw = None if body is None else json.dumps(body).encode()
    headers = {'Content-Type': 'application/json'}
    if key:
        headers['Authorization'] = 'Bearer ' + key
    req = urllib.request.Request(base + path, data=raw, headers=headers, method=method)
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.code, response.read(), response.headers


def ready(base):
    """等待当前容器的正式 HTTP 采集状态；仅启动网络拒绝重试，其他错误或超时明确失败。"""
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        try:
            code, raw, headers = request(base, API + '/status')
        except urllib.error.URLError:
            time.sleep(.1)
            continue
        if code == 200:
            state = json.loads(raw)
            assert state['available'] and state['capture_mode'] == 'http_execution'
            assert state['responses_websocket'] is False
            assert state['state']['state'] == 'active', 'container collector not active'
            return headers
        assert code == 404, 'unexpected management rejection during startup'
        time.sleep(.1)
    raise RuntimeError('official container did not become ready')


def search(base, keyword=''):
    """只经 POST 正文查询有限记录；鉴权、状态或 JSON 错误直接失败。"""
    code, raw, _ = request(base, API + '/records/search', 'POST', {'q': keyword, 'limit': 100})
    assert code == 200, 'POST search failed'
    return json.loads(raw)


def send(base, marker):
    """发送含禁存系统/历史标记的合成用户输入；只检查调用成功，不持久完整请求。"""
    body = {'model': 'container-model', 'messages': [{'role': 'system', 'content': 'FORBIDDEN_CONTAINER_SYSTEM'}, {'role': 'user', 'content': 'FORBIDDEN_CONTAINER_HISTORY'}, {'role': 'user', 'content': marker}]}
    code, _, _ = request(base, '/v1/chat/completions', 'POST', body, CLIENT)
    assert code == 200, 'synthetic container model call failed'


def run(args):
    """在独立目录启动非 root 官方容器，验证发现、鉴权、保存和重启；失败仍停止并保留容器。"""
    root = pathlib.Path(args.output).resolve()
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    os.chown(root, args.uid, args.uid)
    plugin = pathlib.Path(args.plugin).resolve()
    assert plugin.is_file(), 'Linux plugin is missing'
    image = json.loads(docker('image', 'inspect', args.image))[0]
    server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    config = f'''config-version: 8
server:
  host: 127.0.0.1
  port: {port}
management:
  secret-key: {ADMIN}
  disable-control-panel: true
  disable-auto-update-panel: true
access:
  api-keys: [{CLIENT}]
auth-dir: /verify/auth
logging-to-file: false
request-log: false
plugins:
  enabled: true
  dir: /plugins
  configs:
    request-input-audit:
      enabled: true
      state-dir: /verify/state
      budget-bytes: 1073741824
openai-compatibility:
  - name: container-upstream
    base-url: http://127.0.0.1:{server.server_port}/v1
    api-key-entries:
      - api-key: synthetic-container-upstream-only
    models:
      - name: container-model
        alias: container-model
'''
    config_path = root / 'config.yaml'
    config_path.write_text(config)
    config_path.chmod(0o600)
    os.chown(config_path, args.uid, args.uid)
    name = 'cpa-http-lite-' + str(time.time_ns())
    started = False
    result = {'image': args.image, 'image_id': image['Id'], 'repo_digests': image['RepoDigests'], 'container': name, 'uid': args.uid, 'plugin_sha256': hashlib.sha256(plugin.read_bytes()).hexdigest(), 'passed': False}
    try:
        docker('run', '-d', '--name', name, '--network', 'host', '--user', f'{args.uid}:{args.uid}', '--workdir', '/verify', '--env', 'HOME=/verify', '--mount', f'type=bind,source={root},target=/verify', '--mount', f'type=bind,source={plugin.parent},target=/plugins,readonly', '--entrypoint', '/CLIProxyAPI/CLIProxyAPI', args.image, '--config', '/verify/config.yaml', '--local-model')
        started = True
        base = f'http://127.0.0.1:{port}'
        headers = ready(base)
        version, commit = headers.get('X-CPA-VERSION', ''), headers.get('X-CPA-COMMIT', '')
        assert version.lstrip('v') == '8.0.27', 'official image version mismatch'
        assert commit and 'bf9341629ef2300ac9d846a2e80fe2756f63a671'.startswith(commit), 'official image commit mismatch'
        result.update(version=version, commit=commit)
        for key in ['', CLIENT]:
            code, _, _ = request(base, API + '/status', key=key)
            assert code in [401, 403], 'management data not protected'
        assert search(base)['total'] == 0
        send(base, 'PROBE-CONTAINER-DISCOVERY')
        code, raw, _ = request(base, API + '/objects')
        assert code == 200
        objects = json.loads(raw)['objects']
        assert len(objects) == 1 and search(base)['total'] == 0, 'discovery saved unexpected input'
        object_id = objects[0]['id']
        code, _, _ = request(base, API + '/monitor', 'POST', {'object_id': object_id, 'name': 'container synthetic'})
        assert code == 200
        for round in range(2):
            marker = f'PROBE-CONTAINER-RECORD-{round}'
            send(base, marker)
            deadline = time.monotonic() + 5
            while search(base, marker)['total'] != 1:
                assert time.monotonic() < deadline, 'container input did not save'
                time.sleep(.05)
        assert search(base)['total'] == 2
        for row in search(base)['records']:
            code, raw, _ = request(base, API + '/detail?id=' + row['id'])
            detail = json.loads(raw)
            assert code == 200 and detail['text'].startswith('PROBE-CONTAINER-RECORD-')
        seed = root / 'state/caller-scope-v1.seed'
        before_seed = hashlib.sha256(seed.read_bytes()).digest()
        docker('restart', '--time', '15', name)
        ready(base)
        assert search(base)['total'] == 2, 'restart lost history'
        code, raw, _ = request(base, API + '/objects')
        assert code == 200 and any(o['id'] == object_id and o['monitored'] for o in json.loads(raw)['objects'])
        assert hashlib.sha256(seed.read_bytes()).digest() == before_seed, 'restart rotated identity'
        logs = subprocess.check_output(['docker', 'logs', name], stderr=subprocess.STDOUT)
        assert b'PROBE-CONTAINER-RECORD' not in logs, 'search/body text entered host logs'
        for path in (root / 'state').rglob('*'):
            if path.is_file() and path != seed:
                data = path.read_bytes()
                assert all(token.encode() not in data for token in [ADMIN, CLIENT, 'FORBIDDEN_CONTAINER_']), 'state contains forbidden data'
        result.update(passed=True, records=2, discovery_saved=False, management_protected=True, seed_unchanged=True, restart_restored=True, keyword_absent_from_logs=True)
    finally:
        if started:
            docker('stop', '--time', '15', name)
            result['stopped'] = docker('inspect', '--format', '{{.State.Running}}', name) == 'false'
        server.shutdown()
        server.server_close()
        thread.join()
        (root / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plugin', required=True)
    parser.add_argument('--image', default='eceasy/cli-proxy-api:v8.0.27')
    parser.add_argument('--output', required=True, help='必须尚不存在的新目录')
    parser.add_argument('--uid', type=int, default=1000)
    run(parser.parse_args())
