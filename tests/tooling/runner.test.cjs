// 本文件通过 runner 的命令行和子进程就绪协议验证故障处理；不测试业务私有实现，不自动删除夹具。
const test = require('node:test');
const assert = require('node:assert/strict');
const {spawn} = require('node:child_process');
const fs = require('node:fs/promises');
const path = require('node:path');
const root = path.resolve(__dirname, '../..');

/** 在独立保留目录执行 runner 的公开 CLI；args 为参数，返回退出码与有限诊断，超时或启动失败拒绝 Promise。 */
async function runCommand(args) {
  await fs.mkdir(path.join(root, '.test-data'), {recursive:true,mode:0o700});
  const output = await fs.mkdtemp(path.join(root, '.test-data/runner-'));
  return new Promise((resolve,reject) => {
    const child = spawn(process.execPath, [path.join(root,'scripts/browser-runner.cjs'), '--output',output,...args], {cwd:root,env:process.env});
    let stdout='',stderr='';
    const timer=setTimeout(()=>{child.kill('SIGTERM');reject(new Error('公开 CLI 在验收期限内未退出'));},15000);
    child.stdout.on('data',chunk=>{stdout=(stdout+chunk).slice(-16384);});
    child.stderr.on('data',chunk=>{stderr=(stderr+chunk).slice(-16384);});
    child.once('error',error=>{clearTimeout(timer);reject(error);});
    child.once('close',(code,signal)=>{clearTimeout(timer);resolve({code,signal,stdout,stderr,output});});
  });
}

/** 创建可执行的本地就绪协议夹具；code 是合成子进程行为，返回路径，写入失败抛错，不清理数据。 */
async function fixture(code) {
  const directory=await fs.mkdtemp(path.join(root,'.test-data/demo-fixture-'));
  const script=path.join(directory,'demo');
  await fs.writeFile(script,'#!/usr/bin/env node\n'+code,{mode:0o700});
  return {script,directory};
}

// TestRunnerTimeoutStopsDemo 验证未发就绪事件的子进程超时后退出，证据不含秘密。
test('runner 就绪超时会正常停止本次子进程',async()=>{
  const demo=await fixture(`const fs=require('node:fs');fs.writeFileSync(__dirname+'/pid',String(process.pid));process.stderr.write('synthetic-admin-audit-test-only');process.on('SIGTERM',()=>{fs.writeFileSync(__dirname+'/stopped','yes');process.exit(0);});setInterval(()=>{},1000);`);
  const result=await runCommand(['--demo',demo.script,'--ready-timeout-ms','2000','--startup-only']);
  assert.equal(result.code,1);
  assert.match(result.stderr,/ready_timeout/);
  assert.equal(await fs.readFile(path.join(demo.directory,'stopped'),'utf8'),'yes');
  assert.doesNotMatch(result.stderr,/synthetic-admin-audit-test-only/);
  const pid=Number(await fs.readFile(path.join(demo.directory,'pid'),'utf8'));
  assert.throws(()=>process.kill(pid,0),/ESRCH/);
});

// TestRunnerReadyShutdown 验证收到公开 ready 后正常退出，输出证据而不保存子进程诊断。
test('runner 收到 ready 后正常停止并保存有限证据',async()=>{
  const demo=await fixture(`process.on('SIGTERM',()=>process.exit(0));process.stdout.write(JSON.stringify({event:'ready',url:'http://127.0.0.1:12345/management.html'})+'\\n');setInterval(()=>{},1000);`);
  const result=await runCommand(['--demo',demo.script,'--startup-only']);
  assert.equal(result.code,0);
  const report=JSON.parse(await fs.readFile(path.join(result.output,'result.json'),'utf8'));
  assert.equal(report.result,'passed');assert.equal(report.cleanup,true);
});

// TestRealDemoReady 验证本项目真实合成宿主经健康检查后发 ready，runner 正常停止实例。
test('真实 demo 输出机器就绪并正常回收宿主',async()=>{
  const result=await runCommand(['--ready-timeout-ms','5000','--startup-only']);
  assert.equal(result.code,0,result.stderr);
  const report=JSON.parse(await fs.readFile(path.join(result.output,'result.json'),'utf8'));
  assert.equal(report.ready,true);assert.equal(report.cleanup,true);
});

// TestRunnerInterruptedStartup 验证用户中断未就绪的runner时会回收其demo，不遗留服务。
test('中断等待就绪会回收本次demo',async()=>{
  const demo=await fixture(`const fs=require('node:fs');fs.writeFileSync(__dirname+'/pid',String(process.pid));process.on('SIGTERM',()=>{fs.writeFileSync(__dirname+'/stopped','yes');process.exit(0)});setInterval(()=>{},1000);`);
  const output=await fs.mkdtemp(path.join(root,'.test-data/interrupted-runner-'));
  const child=spawn(process.execPath,[path.join(root,'scripts/browser-runner.cjs'),'--output',output,'--demo',demo.script,'--startup-only'],{cwd:root});
  child.stdout.resume();child.stderr.resume();
  const deadline=Date.now()+3000;
  while(Date.now()<deadline){try{await fs.stat(path.join(demo.directory,'pid'));break;}catch{await new Promise(resolve=>setTimeout(resolve,20));}}
  const pid=Number(await fs.readFile(path.join(demo.directory,'pid'),'utf8'));
  const closed=new Promise(resolve=>child.once('close',(code,signal)=>resolve({code,signal})));
  child.kill('SIGTERM');
  const result=await closed;
  // 先确认子进程是否还活着；测试自己的失败清理也只终止本测试创建的PID。
  let alive=true;try{process.kill(pid,0);}catch{alive=false;}
  if(alive)process.kill(pid,'SIGTERM');
  assert.equal(result.code,1);
  assert.equal(alive,false,'runner退出却遗留demo');
  assert.equal(await fs.readFile(path.join(demo.directory,'stopped'),'utf8'),'yes');
});

// TestRunnerRejectsAbnormalDemoExit 验证ready后的异常退出不能被“已收到就绪”掩盖成通过。
test('ready 后 demo 非零退出不能报告验收通过',async()=>{
  const demo=await fixture(`process.on('SIGTERM',()=>process.exit(7));process.stdout.write(JSON.stringify({event:'ready',url:'http://127.0.0.1:12345/management.html'})+'\\n');setInterval(()=>{},1000);`);
  const result=await runCommand(['--demo',demo.script,'--startup-only']);
  assert.equal(result.code,1);
  const report=JSON.parse(await fs.readFile(path.join(result.output,'result.json'),'utf8'));
  assert.equal(report.result,'failed');
});

// TestRunnerStopsOwnedProcessGroup 验证demo卡住时兜底只回收它创建的进程组，不留下子服务。
test('卡住的demo及其自建子进程组会被有界回收',async()=>{
  const demo=await fixture(`const fs=require('node:fs'),{spawn}=require('node:child_process');const sub=spawn(process.execPath,['-e','process.on("SIGTERM",()=>{});setInterval(()=>{},1000)'],{stdio:'ignore'});fs.writeFileSync(__dirname+'/pid',String(sub.pid));process.on('SIGTERM',()=>{});setInterval(()=>{},1000);`);
  const result=await runCommand(['--demo',demo.script,'--ready-timeout-ms','1000','--startup-only']);
  assert.equal(result.code,1);
  const pid=Number(await fs.readFile(path.join(demo.directory,'pid'),'utf8'));
  const deadline=Date.now()+2000;
  let alive=true;
  while(Date.now()<deadline){try{process.kill(pid,0);}catch{alive=false;break;}await new Promise(resolve=>setTimeout(resolve,20));}
  if(alive)process.kill(pid,'SIGKILL');
  assert.equal(alive,false,'回收demo后遗留了其子服务');
});

// TestRunnerRejectsUnknownOptions 验证无效选项明确失败而不是忽略后启动真实实例。
test('runner 拒绝未知选项，不启动真实验收',async()=>{
  const result=await runCommand(['--unknown-option']);
  assert.equal(result.code,2);
  assert.match(result.stderr,/unknown_option/);
});
