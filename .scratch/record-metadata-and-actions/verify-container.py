#!/usr/bin/env python3
"""复用官方容器辅助验证思考元数据及模型目录；仅停止容器，保留独立状态与有限结果。"""

import argparse
import hashlib
import http.server
import importlib.util
import json
import os
import pathlib
import socket
import subprocess
import threading
import time


def send(helper, base, model, marker, reasoning):
    """提交指定模型、文本及可选思考等级；成功无返回值，网络或 HTTP 错误使验收失败。"""
    body = {
        'model': model,
        'messages': [
            {'role': 'system', 'content': 'FORBIDDEN_METADATA_SYSTEM'},
            {'role': 'user', 'content': 'FORBIDDEN_METADATA_HISTORY'},
            {'role': 'user', 'content': marker},
        ],
    }
    if reasoning:
        body['reasoning_effort'] = reasoning['effort']
    code, _, _ = helper.request(base, '/v1/chat/completions', 'POST', body, helper.CLIENT)
    assert code == 200, 'metadata model call failed'


def models(helper, base):
    """读取受保护模型目录并返回数组；HTTP、缺字段、空值或字段错型直接失败。"""
    code, raw, _ = helper.request(base, helper.API + '/models')
    assert code == 200, 'models query failed'
    value = json.loads(raw)['models']
    assert isinstance(value, list) and all(isinstance(item, str) for item in value), 'models must be an array'
    return value


def check_records(helper, base, expected, object_id):
    """按独立预期检查所有列表、详情和模型目录；返回记录 ID 集合，数据不符或查询失败抛错。"""
    page = helper.search(base)
    assert page['total'] == len(expected) and len(page['records']) == len(expected), 'wrong saved record count'
    found = set()
    identities = set()
    for row in page['records']:
        marker = row['preview']
        assert marker in expected and marker not in found, 'unexpected or duplicate preview'
        found.add(marker)
        model, reasoning = expected[marker]
        assert row['model'] == model and row['object_id'] == object_id, 'list metadata changed'
        assert row['request_reasoning'] == reasoning, 'list reasoning changed'
        assert row.get('text', '') == '', 'list exposed complete text'
        code, raw, _ = helper.request(base, helper.API + '/detail?id=' + row['id'])
        assert code == 200, 'detail query failed'
        detail = json.loads(raw)
        assert detail['id'] == row['id'] and detail['object_id'] == object_id, 'detail identity changed'
        assert detail['text'] == marker and detail['model'] == model, 'detail text or model changed'
        assert detail['request_reasoning'] == reasoning, 'detail reasoning changed'
        identities.add(row['id'])
    assert found == set(expected), 'saved markers incomplete'
    assert models(helper, base) == sorted({item[0] for item in expected.values()}), 'models not sorted and deduplicated'
    code, raw, _ = helper.request(base, helper.API + '/records/search', 'POST', {'model': 'metadata-zeta', 'limit': 1, 'offset': 1})
    assert code == 200 and json.loads(raw)['total'] == 2, 'filtered pagination failed'
    assert models(helper, base) == ['metadata-Alpha', 'metadata-zeta'], 'filter or page changed models'
    return identities


def run(helper, args):
    """在新独立目录启动固定非 root 镜像并验证保存与重启；失败也停止容器，不删除文件或输出正文。"""
    assert args.uid > 0, 'non-root uid required'
    assert args.image == 'eceasy/cli-proxy-api:v8.0.27', 'fixed official image required'
    root = pathlib.Path(args.output).resolve()
    root.mkdir(mode=0o700, parents=True, exist_ok=False)
    os.chown(root, args.uid, args.uid)
    plugin = pathlib.Path(args.plugin).resolve()
    assert plugin.is_file(), 'Linux plugin is missing'
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
request-retry: 0
plugins:
  enabled: true
  dir: /plugins
  configs:
    request-input-audit:
      enabled: true
      state-dir: /verify/state
      budget-bytes: 1073741824
openai-compatibility:
  - name: metadata-container-upstream
    base-url: http://127.0.0.1:{upstream.server_port}/v1
    api-key-entries:
      - api-key: synthetic-container-upstream-only
    models:
      - name: container-model
        alias: metadata-zeta
      - name: container-model
        alias: metadata-Alpha
'''
    config_path = root / 'config.yaml'
    config_path.write_text(config)
    config_path.chmod(0o600)
    os.chown(config_path, args.uid, args.uid)
    name = 'cpa-record-metadata-' + str(time.time_ns())
    result = {
        'passed': False, 'image': args.image, 'image_id': image['Id'],
        'repo_digests': image['RepoDigests'], 'architecture': image['Architecture'],
        'uid': args.uid, 'container': name,
        'plugin_sha256': hashlib.sha256(plugin.read_bytes()).hexdigest(),
    }
    started = False
    try:
        helper.docker('run', '-d', '--name', name, '--network', 'host', '--user', f'{args.uid}:{args.uid}', '--workdir', '/verify', '--env', 'HOME=/verify', '--mount', f'type=bind,source={root},target=/verify', '--mount', f'type=bind,source={plugin.parent},target=/plugins,readonly', '--entrypoint', '/CLIProxyAPI/CLIProxyAPI', args.image, '--config', '/verify/config.yaml', '--local-model')
        started = True
        base = f'http://127.0.0.1:{port}'
        headers = helper.ready(base)
        assert headers.get('X-CPA-VERSION', '').lstrip('v') == '8.0.27', 'wrong host version'
        commit = headers.get('X-CPA-COMMIT', '')
        assert commit and 'bf9341629ef2300ac9d846a2e80fe2756f63a671'.startswith(commit), 'wrong host commit'
        assert helper.docker('inspect', '--format', '{{.Config.User}}', name) == f'{args.uid}:{args.uid}', 'wrong container user'
        assert models(helper, base) == [] and helper.search(base)['total'] == 0, 'new state was not empty'
        for key in ['', helper.CLIENT]:
            code, _, _ = helper.request(base, helper.API + '/models', key=key)
            assert code in [401, 403], 'models not management protected'
        send(helper, base, 'metadata-zeta', 'FORBIDDEN_METADATA_DISCOVERY', {})
        code, raw, _ = helper.request(base, helper.API + '/objects')
        assert code == 200, 'discovery query failed'
        objects = json.loads(raw)['objects']
        assert len(objects) == 1 and helper.search(base)['total'] == 0, 'discovery saved input'
        object_id = objects[0]['id']
        code, _, _ = helper.request(base, helper.API + '/monitor', 'POST', {'object_id': object_id, 'name': 'metadata container'})
        assert code == 200, 'monitor selection failed'
        prefix = 'PROBE-METADATA-' + str(time.time_ns())
        expected = {
            prefix + '-high': ('metadata-zeta', {'effort': 'high'}),
            prefix + '-low': ('metadata-Alpha', {'effort': 'low'}),
            prefix + '-empty': ('metadata-zeta', {}),
        }
        for marker, (model, reasoning) in expected.items():
            send(helper, base, model, marker, reasoning)
        deadline = time.monotonic() + 5
        while helper.search(base, prefix + '-empty')['total'] != 1:
            assert time.monotonic() < deadline, 'FIFO barrier did not save'
            time.sleep(.05)
        identities = check_records(helper, base, expected, object_id)
        seed = root / 'state/caller-scope-v1.seed'
        before_seed = hashlib.sha256(seed.read_bytes()).digest()
        helper.docker('restart', '--time', '15', name)
        helper.ready(base)
        assert check_records(helper, base, expected, object_id) == identities, 'restart changed record identities'
        assert hashlib.sha256(seed.read_bytes()).digest() == before_seed, 'restart changed caller seed'
        code, raw, _ = helper.request(base, helper.API + '/objects')
        assert code == 200 and any(item['id'] == object_id and item['monitored'] for item in json.loads(raw)['objects']), 'restart lost monitored identity'
        logs = subprocess.check_output(['docker', 'logs', name], stderr=subprocess.STDOUT)
        assert all(token.encode() not in logs for token in [prefix, helper.ADMIN, helper.CLIENT, 'FORBIDDEN_METADATA_']), 'host logs contain private markers'
        for path in (root / 'state').rglob('*'):
            if path.is_file() and path != seed:
                raw = path.read_bytes()
                assert all(token.encode() not in raw for token in [helper.ADMIN, helper.CLIENT, 'FORBIDDEN_METADATA_']), 'state contains forbidden input'
        result.update(passed=True, two_models=True, distinct_efforts=True, empty_reasoning_object=True, sorted_deduplicated=True, models_management_protected=True, list_detail_consistent=True, filter_page_independent=True, restart_restored=True, seed_unchanged=True, private_markers_absent=True)
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
    parser.add_argument('--output', required=True, help='必须尚不存在的新独立目录')
    parser.add_argument('--uid', type=int, default=1000)
    parser.add_argument('--image', default='eceasy/cli-proxy-api:v8.0.27')
    args = parser.parse_args()
    specification = importlib.util.spec_from_file_location('container_helpers', args.helpers)
    helper = importlib.util.module_from_spec(specification)
    specification.loader.exec_module(helper)
    run(helper, args)
