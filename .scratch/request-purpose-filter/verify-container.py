#!/usr/bin/env python3
"""复用官方容器验收的传输辅助，在非 root 宿主中验证用途过滤和重启；保留容器与数据。"""

import argparse
import hashlib
import http.server
import importlib.util
import json
import os
import pathlib
import socket
import threading
import time
import urllib.request


def send(helper, base, marker, purpose):
    """向合成上游提交用户文本与可选用途头；成功无返回值，网络或 HTTP 错误直接失败。"""
    body = json.dumps({'model': 'container-model', 'messages': [{'role': 'user', 'content': marker}]}).encode()
    headers = {'Content-Type': 'application/json', 'Authorization': 'Bearer ' + helper.CLIENT}
    if purpose is not None:
        headers['x-claude-code-request-class'] = purpose
    request = urllib.request.Request(base + '/v1/chat/completions', body, headers)
    with urllib.request.urlopen(request, timeout=5) as response:
        assert response.status == 200, 'purpose changed model response'


def run(helper, args):
    """在新目录内验证辅助发现、用途保存及持久身份；失败也停止并保留本轮容器，不改既有实例。"""
    root = pathlib.Path(args.output).resolve()
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    os.chown(root, args.uid, args.uid)
    plugin = pathlib.Path(args.plugin).resolve()
    assert plugin.is_file(), 'plugin is missing'
    image = json.loads(helper.docker('image', 'inspect', args.image))[0]
    upstream = http.server.ThreadingHTTPServer(('127.0.0.1', 0), helper.Upstream)
    worker = threading.Thread(target=upstream.serve_forever, daemon=True)
    worker.start()
    with socket.socket() as listener:
        listener.bind(('127.0.0.1', 0))
        port = listener.getsockname()[1]
    config = f'''config-version: 8
server:
  host: 127.0.0.1
  port: {port}
management:
  secret-key: {helper.ADMIN}
  disable-control-panel: true
  disable-auto-update-panel: true
access:
  api-keys: [{helper.CLIENT}]
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
    base-url: http://127.0.0.1:{upstream.server_port}/v1
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
    name = 'cpa-purpose-' + str(time.time_ns())
    result = {'passed': False, 'image': args.image, 'image_id': image['Id'], 'architecture': image['Architecture'], 'uid': args.uid, 'container': name, 'plugin_sha256': hashlib.sha256(plugin.read_bytes()).hexdigest()}
    started = False
    try:
        helper.docker('run', '-d', '--name', name, '--network', 'host', '--user', f'{args.uid}:{args.uid}', '--workdir', '/verify', '--env', 'HOME=/verify', '--mount', f'type=bind,source={root},target=/verify', '--mount', f'type=bind,source={plugin.parent},target=/plugins,readonly', '--entrypoint', '/CLIProxyAPI/CLIProxyAPI', args.image, '--config', '/verify/config.yaml', '--local-model')
        started = True
        base = f'http://127.0.0.1:{port}'
        headers = helper.ready(base)
        assert headers.get('X-CPA-VERSION', '').lstrip('v') == '8.0.27', 'wrong host version'
        assert 'bf9341629ef2300ac9d846a2e80fe2756f63a671'.startswith(headers.get('X-CPA-COMMIT', 'missing')), 'wrong host commit'
        send(helper, base, 'FORBIDDEN_PURPOSE_DISCOVERY', 'auxiliary')
        code, raw, _ = helper.request(base, helper.API + '/objects')
        objects = json.loads(raw)['objects']
        assert code == 200 and len(objects) == 1 and helper.search(base)['total'] == 0
        object_id = objects[0]['id']
        code, _, _ = helper.request(base, helper.API + '/monitor', 'POST', {'object_id': object_id, 'name': 'purpose container'})
        assert code == 200
        for marker, purpose in [('PROBE-PURPOSE-MAIN', 'main'), ('FORBIDDEN_PURPOSE_AUXILIARY', 'auxiliary'), ('PROBE-PURPOSE-BARRIER', None)]:
            send(helper, base, marker, purpose)
        deadline = time.monotonic() + 5
        while helper.search(base, 'PROBE-PURPOSE-BARRIER')['total'] != 1:
            assert time.monotonic() < deadline, 'FIFO barrier did not save'
            time.sleep(.05)
        records = helper.search(base)
        assert records['total'] == 2, 'auxiliary body was saved'
        assert {row['purpose'] for row in records['records']} == {'main', 'unknown'}
        for row in records['records']:
            code, raw, _ = helper.request(base, helper.API + '/detail?id=' + row['id'])
            assert code == 200 and json.loads(raw)['purpose'] == row['purpose']
        seed = root / 'state/caller-scope-v1.seed'
        before_seed = hashlib.sha256(seed.read_bytes()).digest()
        helper.docker('restart', '--time', '15', name)
        helper.ready(base)
        restored = helper.search(base)
        assert restored['total'] == 2 and {row['purpose'] for row in restored['records']} == {'main', 'unknown'}
        assert hashlib.sha256(seed.read_bytes()).digest() == before_seed
        for path in (root / 'state').rglob('*'):
            if path.is_file() and path != seed:
                data = path.read_bytes()
                assert all(token.encode() not in data for token in [helper.ADMIN, helper.CLIENT, 'FORBIDDEN_PURPOSE_'])
        result.update(passed=True, discovery_saved=False, auxiliary_skipped=True, main_and_unknown=True, restart_restored=True, seed_unchanged=True)
    finally:
        if started:
            helper.docker('stop', '--time', '15', name)
            result['stopped'] = helper.docker('inspect', '--format', '{{.State.Running}}', name) == 'false'
        upstream.shutdown()
        upstream.server_close()
        worker.join()
        (root / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--helpers', required=True, help='仓库内既有 official-http-input/verify-container.py')
    parser.add_argument('--plugin', required=True)
    parser.add_argument('--image', default='eceasy/cli-proxy-api:v8.0.27')
    parser.add_argument('--output', required=True, help='必须尚不存在的新目录')
    parser.add_argument('--uid', type=int, default=1000)
    args = parser.parse_args()
    specification = importlib.util.spec_from_file_location('container_helpers', args.helpers)
    helper = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(helper)
    run(helper, args)
