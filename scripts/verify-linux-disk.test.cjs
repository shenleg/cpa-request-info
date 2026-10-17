// 本文件验证磁盘工具公开 CLI 及 SQLite 记录快照；不挂载、不提权、不启动宿主，不删除夹具。
const test = require('node:test');
const assert = require('node:assert/strict');
const {spawnSync} = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '..');

/** 执行磁盘工具公开CLI；args为参数，返回退出状态及有限诊断，启动失败抛错。 */
function run(args) {
  const result = spawnSync('bash', [path.join(__dirname, 'verify-linux-disk.sh'), ...args], {encoding:'utf8', timeout:5000});
  if (result.error) throw result.error;
  return result;
}

/** 创建保留的CLI文件系统夹具；返回源码和输出目录，I/O失败抛错，不启动合成宿主。 */
function fixture() {
  fs.mkdirSync(path.join(root, '.test-data'), {recursive:true, mode:0o700});
  const base = fs.mkdtempSync(path.join(root, '.test-data/linux-disk-cli-'));
  const source = path.join(base, 'source');
  const output = path.join(base, 'output');
  fs.mkdirSync(path.join(source, '.build/bin'), {recursive:true});
  fs.mkdirSync(path.join(source, '.build/plugins'), {recursive:true});
  fs.writeFileSync(path.join(source, '.build/bin/cliproxyapi'), '', {mode:0o700});
  fs.writeFileSync(path.join(source, '.build/plugins/request-input-audit.so'), '');
  fs.mkdirSync(output);
  return {base, source, output};
}

/** 生成公开校验命令；f为夹具，返回参数，不提权或挂载，无异常。 */
function args(f) {
  return ['--source', f.source, '--output', f.output, '--user', require('node:os').userInfo().username, '--validate-only'];
}

test('输出目录已有文件时拒绝且保留文件', () => {
  const f = fixture();
  fs.writeFileSync(path.join(f.output, 'keep'), 'untouched');
  const result = run(args(f));
  assert.equal(result.status, 2);
  assert.match(result.stderr, /output_not_empty/);
  assert.equal(fs.readFileSync(path.join(f.output, 'keep'), 'utf8'), 'untouched');
});

test('校验拒绝root、相对路径、符号链接及缺少构建产物，合法目录不被修改', () => {
  const f = fixture();
  const valid = args(f);
  assert.equal(run([...valid, '--user', 'root']).status, 2);
  assert.match(run([...valid, '--source', '.']).stderr, /source_absolute/);
  fs.symlinkSync(f.output, path.join(f.base, 'link'));
  assert.match(run([...valid, '--output', path.join(f.base, 'link')]).stderr, /output_symlink/);
  assert.match(run([...valid, '--source', f.output]).stderr, /missing_artifacts/);
  assert.equal(run(valid).status, 0);
  assert.deepEqual(fs.readdirSync(f.output), []);
});

test('缺参、未知用户及不存在的输出目录明确失败且不创建目录', () => {
  const f = fixture();
  assert.match(run([]).stderr, /missing_source/);
  assert.match(run([...args(f), '--user']).stderr, /expected one argument/);
  assert.match(run([...args(f), '--user', 'cpa-nonexistent-user-verify-only']).stderr, /unknown_user/);
  const absent = path.join(f.base, 'absent');
  assert.match(run([...args(f), '--output', absent]).stderr, /output_directory_required/);
  assert.equal(fs.existsSync(absent), false);
});

test('未知容量参数明确拒绝，不能扩大固定64MiB卷', () => {
  // 工具只能创建固定容量卷，错误参数不能被忽略后开始真实验收。
  const result = run(['--size', '1G']);
  assert.equal(result.status, 2);
  assert.match(result.stderr, /unrecognized arguments/);
});

test('SQLite快照读取有效记录，缺库、缺journal、损坏与错误schema必须失败', () => {
  const f = fixture();
  // 使用 Python 标准库构造真实 SQLite；记录包含 NUL，不能按 JSONL 扫描或比较整个数据库字节。
  const code = String.raw`
import importlib.util
import pathlib
import sqlite3
import sys

spec = importlib.util.spec_from_file_location("disk_verifier", sys.argv[1])
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
root = pathlib.Path(sys.argv[2])
valid = root / "valid"
valid.mkdir()
connection = sqlite3.connect(valid / "records.sqlite")
connection.executescript("""
PRAGMA journal_mode=PERSIST;
PRAGMA application_id=0x43504149;
PRAGMA user_version=1;
CREATE TABLE records(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL,object_id TEXT NOT NULL,received_at TEXT NOT NULL,received_sec INTEGER NOT NULL,received_ns INTEGER NOT NULL,protocol TEXT NOT NULL,purpose TEXT NOT NULL,model TEXT NOT NULL,request_reasoning TEXT,text TEXT NOT NULL,truncated INTEGER NOT NULL,extraction_incomplete INTEGER NOT NULL) STRICT;
""")
text = "sqlite-nul-\x00%_中文"
connection.execute("INSERT INTO records VALUES(1,'control','object','2030-01-01T00:00:00Z',1893456000,0,'chat-completions','','model',NULL,?,0,0)", (text,))
connection.commit()
connection.close()
expected = module.sqlite_records(valid)
assert len(expected) == 1 and expected[0][1] == "control" and expected[0][10] == text
connection = sqlite3.connect(valid / "records.sqlite")
connection.execute("PRAGMA cache_size=128")
connection.close()
assert module.sqlite_records(valid) == expected

def rejects(directory):
    """断言快照对无效磁盘边界明确失败；directory 指向保留夹具，不删除或改写文件。"""
    try:
        module.sqlite_records(directory)
    except (AssertionError, sqlite3.DatabaseError):
        return
    raise AssertionError("无效SQLite被空扫描冒充成功")

missing = root / "missing"
missing.mkdir()
rejects(missing)
(valid / "records.sqlite-journal").rename(valid / "journal.preserved")
rejects(valid)
corrupt = root / "corrupt"
corrupt.mkdir()
(corrupt / "records.sqlite").write_bytes(b"invalid sqlite header")
(corrupt / "records.sqlite-journal").write_bytes(b"preserved fault")
rejects(corrupt)
wrong = root / "wrong-schema"
wrong.mkdir()
connection = sqlite3.connect(wrong / "records.sqlite")
connection.executescript("PRAGMA journal_mode=PERSIST; PRAGMA application_id=0x43504149; PRAGMA user_version=1; CREATE TABLE records(id TEXT);")
connection.close()
rejects(wrong)
print("SQLite fixture boundaries verified")
`;
  const result = spawnSync('python3', ['-I', '-c', code, path.join(__dirname, 'verify-linux-disk.py'), f.output], {encoding:'utf8', timeout:5000});
  if (result.error) throw result.error;
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /SQLite fixture boundaries verified/);
});
