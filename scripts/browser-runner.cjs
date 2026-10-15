// 本命令驱动合成本地实例；子进程仅在本轮生命周期存在，证据不含原始 stderr、认证材料或正文。
'use strict';
const {parseArgs} = require('node:util');
const {spawn} = require('node:child_process');
const fs = require('node:fs/promises');
const path = require('node:path');
const readline = require('node:readline');
const root = path.resolve(__dirname,'..');
// 本地宿主通常秒级就绪，默认30秒兼容冷启动；0.1至120秒防止误配置成零或无界等待。
const DEFAULT_READY_MS=30000, MIN_READY_MS=100, MAX_READY_MS=120000;
// ready 仅含URL/路径/PID，64KiB已远大于合法事件；阻止异常子进程无界堆积启动输出。
const READY_OUTPUT_LIMIT=65536;

/** 解析公开参数，返回有限的输出/启动配置；未知选项或非法期限抛出固定错误，不启动进程。 */
function parseOptions() {
  let values;
  try {values=parseArgs({options:{output:{type:'string'},demo:{type:'string'},'ready-timeout-ms':{type:'string'},'startup-only':{type:'boolean',default:false}},strict:true}).values;}
  catch {throw new Error('unknown_option');}
  const readyTimeout=Number(values['ready-timeout-ms']??DEFAULT_READY_MS);
  if (!Number.isInteger(readyTimeout)||readyTimeout<MIN_READY_MS||readyTimeout>MAX_READY_MS) throw new Error('invalid_ready_timeout');
  return {output:values.output,demo:path.resolve(values.demo??path.join(root,'.build/bin/audit-demo')),readyTimeout,startupOnly:values['startup-only']};
}

/** 启动 demo 并等待其 ready JSON；options 限定期限，返回 ready/stop，失败也停止自身子进程，不打印其 stderr。 */
async function startDemo(options) {
  const child=spawn(options.demo,['--json'],{cwd:root,detached:true,env:{PATH:process.env.PATH,HOME:process.env.HOME,TMPDIR:process.env.TMPDIR??'/tmp',LANG:'en_US.UTF-8'}});
  let ended=false;
  const forwardSignal=()=>{if(!ended)child.kill('SIGTERM');};
  process.on('SIGTERM',forwardSignal);process.on('SIGINT',forwardSignal);
  const removeSignals=()=>{process.off('SIGTERM',forwardSignal);process.off('SIGINT',forwardSignal);};
  const exited=new Promise(resolve=>{child.once('close',(code,signal)=>{ended=true;resolve({code,signal});});});
  child.stderr.on('data',()=>{});
  // demo 最多五秒回收宿主，外层给八秒避免先杀demo留下宿主；只处理本轮进程。
  async function stop() {
    if (!ended) child.kill('SIGTERM');
    let timer;
    const outcome=await Promise.race([exited,new Promise(resolve=>{timer=setTimeout(()=>resolve(null),8000);})]);
    clearTimeout(timer);
    if (outcome===null) {
      // demo 是新进程组的组长；兜底只终止本轮组，连同尚未回收的宿主，不能扫描其他进程。
      try {process.kill(-child.pid,'SIGKILL');} catch {child.kill('SIGKILL');}
      await exited;removeSignals();return false;
    }
    removeSignals();
    return outcome.code===0 && outcome.signal===null;
  }
  let timer;
  let total=0;
  const lines=readline.createInterface({input:child.stdout});
  try {
    const ready=await new Promise((resolve,reject)=>{
      timer=setTimeout(()=>reject(new Error('ready_timeout')),options.readyTimeout);
      child.once('error',()=>reject(new Error('demo_start_failed')));
      child.once('close',()=>reject(new Error('demo_exited_before_ready')));
      child.stdout.on('data',chunk=>{total+=chunk.length;if(total>READY_OUTPUT_LIMIT)reject(new Error('ready_output_limit'));});
      lines.on('line',line=>{
        let event;try{event=JSON.parse(line);}catch{return;}
        if (event.event!=='ready') return;
        try {
          const url=new URL(event.url);
          if (url.protocol!=='http:'||url.hostname!=='127.0.0.1'||!url.port) throw new Error();
          resolve(event);
        } catch {reject(new Error('invalid_ready_url'));}
      });
    });
    return {ready,stop,exited};
  } catch(error) {error.cleanup=await stop();throw error;}
  finally {clearTimeout(timer);lines.close();}
}

/** 执行本轮验收并写结果；options 是已校验参数，返回退出码，所有异常转固定原因并回收进程。 */
async function main(options) {
  await fs.mkdir(path.join(root,'.test-data'),{recursive:true,mode:0o700});
  const output=options.output?path.resolve(options.output):await fs.mkdtemp(path.join(root,'.test-data/verification-'));
  await fs.mkdir(output,{recursive:true,mode:0o700});
  const report={result:'failed',mode:options.startupOnly?'startup':'browser',phase:'startup',scenarios:[],cleanup:false};
  let demo,browser,interrupted=false,startupCleanup=true;
  const interrupt=()=>{interrupted=true;if(browser)void browser.close().catch(()=>{});};
  process.on('SIGINT',interrupt);process.on('SIGTERM',interrupt);
  try {
    demo=await startDemo(options);
    report.ready=true;
    if (!options.startupOnly) {
      report.phase='browser_launch';
      const {chromium}=require('playwright');
      browser=await chromium.launch({headless:true});
      const scenarios=[['smoke',require('../tests/browser/smoke.js')],['truncation',require('../tests/browser/truncation.js')],['lifecycle',require('../tests/browser/lifecycle.js')],['responses-websocket',require('../tests/browser/responses-websocket.js')],['search-browse',require('../tests/browser/search-browse.js')]];
      // 同一宿主保留前轮记录，每个场景独立登录；连续两轮锁定“旧记录假通过”的回归。
      for(let round=1;round<=2;round++)for(const [name,scenario] of scenarios) {
        const context=await browser.newContext({locale:'zh-CN',viewport:{width:1200,height:900}});
        context.setDefaultTimeout(15000);
        context.setDefaultNavigationTimeout(15000);
        const page=await context.newPage();
        try {
          await page.goto(demo.ready.url);
          const screenshot=path.join(output,name+'-'+round+'.png');
          report.phase=name+'-'+round;
          const result=await scenario(page,{screenshot});
          // 截图只取状态/详情标签，不采集完整正文、对象ID或凭据。
          if(name==='smoke')await page.frameLocator('iframe[title="输入审计"]').locator('#message').screenshot({path:screenshot});
          report.scenarios.push({name,round,...result,screenshot:path.basename(screenshot)});
        } finally {await context.close();}
      }
    }
    if(interrupted)throw new Error('interrupted');
    report.result='passed';
  } catch(error) {
    if(error.cleanup===false)startupCleanup=false;
    // 只提取本项目场景文件行号，不保存 Playwright 的调用日志（可能包含输入和认证材料）。
    const source=String(error.stack??'').match(/tests[\\/]browser[\\/](smoke|truncation|lifecycle|responses-websocket|search-browse)\.js:\d+:\d+/);
    if(source)report.location=source[0];
    report.reason=/^(ready_timeout|demo_start_failed|demo_exited_before_ready|ready_output_limit|invalid_ready_url|interrupted)$/.test(error.message)?error.message:'verification_failed';
  } finally {
    try {if(browser)await browser.close();} catch {report.result='failed';report.reason='browser_cleanup_failed';}
    report.cleanup=demo?await demo.stop():startupCleanup;
    process.off('SIGINT',interrupt);process.off('SIGTERM',interrupt);
    if(!report.cleanup){report.result='failed';report.reason='demo_shutdown_failed';}
    await fs.writeFile(path.join(output,'result.json'),JSON.stringify(report,null,2)+'\n',{mode:0o600});
  }
  process.stdout.write(JSON.stringify({result:report.result,report:path.join(output,'result.json')})+'\n');
  if(report.result!=='passed')process.stderr.write(report.reason+' ('+report.phase+')\n');
  return report.result==='passed'?0:1;
}

let options;
try {options=parseOptions();}catch(error){process.stderr.write(error.message+'\n');process.exitCode=2;}
if(options)main(options).then(code=>{process.exitCode=code;}).catch(()=>{process.stderr.write('runner_failed\n');process.exitCode=1;});
