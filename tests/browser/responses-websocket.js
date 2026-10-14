// 本场景通过真实 WebSocket 模型提交和管理页面验证逐条记录、状态标签与上限设置；只使用既有合成凭据。
// page 指向本地合成实例，options 指定标签截图路径；返回有限证据，连接、保存或页面行为不符时抛出固定错误。
module.exports = async (page, options = {}) => {
  /** 核对 condition；失败以 message 抛错，成功无返回值，不输出凭据或正文。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const origin = new URL(page.url()).origin;
  const admin = 'synthetic-admin-audit-test-only';
  const api = origin + '/v0/management/plugins/request-input-audit';
  const marker = 'BROWSER-WS-' + require('node:crypto').randomUUID() + '-';
  const recordIds = new Set();
  const extraScreenshots = [];
  await page.goto(origin + '/management.html');
  await page.waitForFunction(() => document.querySelector('input[type="password"]') || document.querySelector('a[href="#/plugin-pages/request-input-audit/0"]'));
  if (await page.getByRole('textbox', {name:'管理密钥:'}).count()) {
    await page.getByRole('textbox', {name:'管理密钥:'}).fill(admin);
    await page.getByRole('checkbox', {name:'记住密码'}).uncheck();
    await page.getByRole('button', {name:'登录',exact:true}).click();
  }
  await page.getByRole('link', {name:'输入审计',exact:true}).click();
  const frame = page.frameLocator('iframe[title="输入审计"]');
  await frame.getByRole('textbox', {name:'临时管理 Key'}).fill(admin);
  await frame.getByRole('button', {name:'连接',exact:true}).click();
  await frame.getByRole('status').filter({hasText:'管理鉴权通过'}).waitFor();
  const ids = await frame.locator('#objects option').evaluateAll(options => options.map(option => option.value));
  assert(ids.length===2, 'WebSocket 合成实例应仅有两个客户端对象');
  for (const [i,id] of ids.entries()) {
    await frame.locator('#objects').selectOption(id);
    await frame.getByRole('textbox', {name:'名称',exact:true}).fill('WebSocket验收对象' + i);
    const saved = page.waitForResponse(r => r.url().endsWith('/request-input-audit/monitor') && r.request().method()==='POST');
    await frame.getByRole('button', {name:'选择并命名',exact:true}).click();
    assert((await saved).status()===200, 'WebSocket 监控配置未保存');
    await frame.locator('#objects option').filter({hasText:'WebSocket验收对象'+i}).waitFor({state:'attached'});
  }
  /** 读取 path 对应的公开受保护接口，返回 JSON；鉴权或 HTTP 错误抛出固定说明。 */
  async function read(path) {
    const response = await page.request.get(api+path, {headers:{Authorization:'Bearer '+admin}});
    assert(response.status()===200, 'WebSocket 受保护查询失败');
    return response.json();
  }
  /** 等待页面显示 limit 且操作结束；成功无返回值，超时由 Playwright 抛错。 */
  async function displayedLimit(limit) {
    await page.waitForFunction(expected => {
      const doc = document.querySelector('iframe[title="输入审计"]').contentDocument;
      return doc.querySelector('#max-text-bytes').value===String(expected) && !doc.querySelector('#save-settings').disabled;
    }, limit);
  }
  /** 通过页面保存 limit 并核对公开配置，成功无返回值；拒绝或显示不一致抛错。 */
  async function saveLimit(limit) {
    await frame.locator('#max-text-bytes').fill(String(limit));
    const saved = page.waitForResponse(r => r.url().endsWith('/request-input-audit/settings') && r.request().method()==='POST');
    await frame.locator('#save-settings').click();
    assert((await saved).status()===200, 'WebSocket 正文上限未保存');
    await frame.getByRole('status').filter({hasText:'正文上限已保存'}).waitFor();
    await displayedLimit(limit);
    assert((await read('/settings')).max_text_bytes===limit, '页面上限与公开配置不一致');
  }
  /** 从页面刷新并检查持久设置 limit，成功无返回值；查询或显示超时抛错。 */
  async function refreshLimit(limit) {
    const refreshed = page.waitForResponse(r => r.url().endsWith('/request-input-audit/settings') && r.request().method()==='GET');
    await frame.getByRole('button', {name:'刷新已保存记录',exact:true}).click();
    assert((await refreshed).status()===200, 'WebSocket 刷新设置失败');
    await displayedLimit(limit);
    assert((await read('/settings')).max_text_bytes===limit, '刷新丢失正文上限');
  }
  /** 按宿主既有 key query 建立真实本地连接，返回 WebSocket；升级失败或超时抛出固定说明。 */
  async function connect() {
    const url = new URL('/v1/responses', origin);
    url.protocol = 'ws:';
    url.searchParams.set('key', 'synthetic-client-audit-a-only');
    const socket = new WebSocket(url);
    return new Promise((resolve, reject) => {
      // 十秒仅是本地升级验收期限，失败关闭本轮连接，不保存带 Key 的 URL。
      const timer = setTimeout(() => finish(new Error('WebSocket 本地连接超时')), 10000);
      const opened = () => finish();
      const failed = () => finish(new Error('WebSocket 本地连接失败'));
      /** 结束升级等待；error 决定拒绝或返回连接，移除监听，失败时关闭连接，无返回值。 */
      function finish(error) {
        clearTimeout(timer);
        socket.removeEventListener('open', opened);
        socket.removeEventListener('error', failed);
        socket.removeEventListener('close', failed);
        if (error) { socket.close(); reject(error); } else resolve(socket);
      }
      socket.addEventListener('open', opened, {once:true});
      socket.addEventListener('error', failed, {once:true});
      socket.addEventListener('close', failed, {once:true});
    });
  }
  /** 在 socket 提交 input 并等待本次公开完成事件及合成回复；成功无返回值，失败/断开/超时抛错。 */
  async function submit(socket, input) {
    assert(socket.readyState===WebSocket.OPEN, 'WebSocket 提交前连接已断开');
    await new Promise((resolve, reject) => {
      // 有限等待真实模型完成，不能以审计落盘或旧 response.completed 代替本次上游执行。
      const timer = setTimeout(() => finish(new Error('WebSocket 本轮模型没有完成')), 10000);
      const failed = () => finish(new Error('WebSocket 本轮模型连接失败'));
      /** 核对本轮公开 message 的终态与回复；解析/模型失败拒绝等待，其余事件继续等待，不保存原帧。 */
      function received(message) {
        let event;
        try { event = JSON.parse(message.data); }
        catch { finish(new Error('WebSocket 公开事件不是合法 JSON')); return; }
        if (event.type==='error' || event.type==='response.failed') {
          finish(new Error('WebSocket 本轮模型返回失败'));
          return;
        }
        if (event.type==='response.completed') {
          const reply = event.response.output.some(item => item.type==='message' && item.content.some(content => content.type==='output_text' && content.text==='合成上游回复'));
          finish(reply ? null : new Error('WebSocket 本轮没有真实合成模型回复'));
        }
      }
      /** 清理本次等待；error 决定拒绝或完成，无返回值，不输出事件内容。 */
      function finish(error) {
        clearTimeout(timer);
        socket.removeEventListener('message', received);
        socket.removeEventListener('error', failed);
        socket.removeEventListener('close', failed);
        if (error) reject(error); else resolve();
      }
      socket.addEventListener('message', received);
      socket.addEventListener('error', failed, {once:true});
      socket.addEventListener('close', failed, {once:true});
      socket.send(JSON.stringify({type:'response.create',model:'audit-test-model',input}));
    });
  }
  /** 等待 fixture 的本轮记录并核对正文/协议/状态；suffix 区分标签截图，返回详情，超时或不一致抛错。 */
  async function openSavedDetail(fixture, suffix) {
    let record;
    const deadline = Date.now()+10000; // 给本地异步 fsync 十秒，不使用历史条数或固定 sleep。
    while (!record && Date.now()<deadline) {
      const list = await read('/records?limit=100');
      record = list.records.find(r => r.preview.startsWith(fixture.marker));
    }
    assert(record && !recordIds.has(record.id), 'WebSocket 本轮提交没有独立已保存记录');
    recordIds.add(record.id);
    assert(record.protocol==='responses-websocket' && record.truncated===fixture.truncated && record.extraction_incomplete===fixture.incomplete, 'WebSocket 本轮列表协议或状态错误');
    await frame.getByRole('button', {name:'刷新已保存记录',exact:true}).click();
    const row = frame.locator('#records tbody tr').filter({hasText:fixture.marker});
    await row.getByRole('cell', {name:'Responses WebSocket',exact:true}).waitFor();
    await row.getByRole('cell', {name:fixture.label,exact:true}).waitFor();
    const detailResponse = page.waitForResponse(r => r.url().includes('/request-input-audit/detail?') && new URL(r.url()).searchParams.get('id')===record.id);
    await row.getByRole('button', {name:'查看',exact:true}).click();
    const response = await detailResponse;
    assert(response.status()===200, 'WebSocket 本轮详情查询失败');
    const detail = await response.json();
    assert(detail.id===record.id && detail.protocol==='responses-websocket' && detail.model==='audit-test-model' && detail.text===fixture.want && detail.truncated===fixture.truncated && detail.extraction_incomplete===fixture.incomplete, 'WebSocket 列表与详情不对应本轮提交');
    await frame.locator('#detail-meta').filter({hasText:'Responses WebSocket · audit-test-model · '+fixture.label}).waitFor();
    await frame.locator('#detail').filter({hasText:fixture.marker}).waitFor();
    assert((await frame.locator('#detail').textContent())===fixture.want, 'WebSocket 页面正文不是本轮已保存文本');
    if (options.screenshot) {
      const screenshot = suffix ? options.screenshot.replace(/\.png$/, '-'+suffix+'.png') : options.screenshot;
      await frame.locator('#detail-meta').screenshot({path:screenshot});
      if (suffix) extraScreenshots.push(require('node:path').basename(screenshot));
    }
    return detail;
  }
  await displayedLimit(65536);
  assert((await read('/settings')).max_text_bytes===65536, 'WebSocket 默认正文上限应为 65536');
  const socket = await connect();
  const unknown = {type:'future',payload:'FORBIDDEN_BROWSER_WS_UNKNOWN'};
  let original;
  try {
    const plain = marker+'PLAIN';
    await submit(socket, [{role:'user',content:[{type:'input_text',text:plain}]}]);
    original = await openSavedDetail({marker:plain,want:plain,truncated:false,incomplete:false,label:'已保存允许内容'});
    const incomplete = marker+'INCOMPLETE';
    await submit(socket, [{role:'user',content:[{type:'input_text',text:incomplete},{type:'input_image',image_url:'FORBIDDEN_BROWSER_WS_IMAGE'},unknown]}]);
    await openSavedDetail({marker:incomplete,want:incomplete+'[图片]',truncated:false,incomplete:true,label:'提取不完整'}, 'incomplete');
    await saveLimit(7);
    await refreshLimit(7);
    for (const invalid of ['0','1.5']) {
      await frame.locator('#max-text-bytes').fill(invalid);
      await frame.locator('#save-settings').click();
      await frame.getByRole('status').filter({hasText:'正文上限必须是 1–65536 之间的整数'}).waitFor();
      assert((await read('/settings')).max_text_bytes===7, '页面非法上限改变了旧值');
      const rejected = await page.request.post(api+'/settings', {headers:{Authorization:'Bearer '+admin},data:{max_text_bytes:Number(invalid)}});
      assert(rejected.status()===400 && (await read('/settings')).max_text_bytes===7, '公开接口没有拒绝非法上限并保留旧值');
      await refreshLimit(7);
    }
    for (const [i,incomplete] of [false,true].entries()) {
      // 七个 ASCII 字节作为本轮独立标记；其后的四字节字符不能被截成破损 UTF-8。
      const shortMarker = require('node:crypto').randomUUID().replace(/-/g,'').slice(0,7);
      const content = [{type:'input_text',text:shortMarker+'🙂FORBIDDEN_BROWSER_WS_TAIL'}];
      if (incomplete) content.push(unknown);
      await submit(socket, [{role:'user',content}]);
      await openSavedDetail({marker:shortMarker,want:shortMarker,truncated:true,incomplete,label:incomplete?'已截断 · 提取不完整':'已截断'}, i===0?'truncated':'combined');
    }
    const preserved = await read('/detail?id='+encodeURIComponent(original.id));
    assert(preserved.text===plain && !preserved.truncated, '修改上限追溯改变了原有记录');
    await saveLimit(65536);
    await refreshLimit(65536);
    const restored = marker+'RESTORED';
    await submit(socket, [{role:'user',content:[{type:'input_text',text:restored}]}]);
    await openSavedDetail({marker:restored,want:restored,truncated:false,incomplete:false,label:'已保存允许内容'}, 'restored');
    const notice = await frame.locator('#notice').textContent();
    assert(notice.includes('Responses WebSocket'), '页面没有说明 WebSocket 支持');
  } finally {
    socket.close();
  }
  return {extraScreenshots,modelCompleted:true,independentRecords:recordIds.size,websocketProtocolLabels:true,incompleteLabels:true,truncatedLabels:true,combinedLabels:true,defaultMaxTextBytes:65536,reducedMaxTextBytes:7,refreshPreservedLimit:true,invalidLimitsRejected:true,priorRecordPreserved:true,restoredMaxTextBytes:65536,sameConnection:true};
};
