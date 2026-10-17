// 历史场景名 responses-websocket 保留，当前目标是真实 WebSocket 成功但完全不采集；正文设置改用 HTTP 控制记录验证。
// page 指向本地合成实例，options 指定有限状态截图；连接、模型执行或公开记录不符时抛错，不保存凭据和正文。
/** 验证 page 的 HTTP 控制记录、正文上限与 WebSocket 不采集边界；options 指定截图，返回有限证据，行为不符时抛错。 */
module.exports = async (page, options = {}) => {
  /** 核对 condition；失败以 message 抛错，成功无返回值，不输出凭据或正文。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const origin = new URL(page.url()).origin;
  const admin = 'synthetic-admin-audit-test-only';
  const api = origin + '/v0/management/plugins/request-input-audit';
  const headers = {Authorization:'Bearer '+admin};
  const marker = 'BROWSER-WS-NEGATIVE-' + require('node:crypto').randomUUID() + '-';
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
  assert(ids.length===2, 'WebSocket 负例应先有两个 HTTP 已观察对象');
  await frame.locator('#open-objects').click();
  for (const [i,id] of ids.entries()) {
    await frame.locator('#objects').selectOption(id);
    await frame.getByRole('textbox', {name:'名称',exact:true}).fill('HTTP边界验收对象' + i);
    const saved = page.waitForResponse(r => r.url().endsWith('/request-input-audit/monitor') && r.request().method()==='POST');
    await frame.getByRole('button', {name:'选择并命名',exact:true}).click();
    assert((await saved).status()===200, 'HTTP 监控配置未保存');
    await frame.locator('#objects option').filter({hasText:'HTTP边界验收对象'+i}).waitFor({state:'attached'});
  }
  await frame.locator('#close-management').click();
  /** 读取 path 对应的受保护公开接口，返回 JSON；鉴权或 HTTP 错误抛错。 */
  async function read(path) {
    const response = await page.request.get(api+path, {headers});
    assert(response.status()===200, 'HTTP 边界受保护查询失败');
    return response.json();
  }
  /** 通过 POST 查询 filter 指定的记录；返回 JSON，HTTP 错误抛错，不将关键词写入 URL。 */
  async function search(filter) {
    const response = await page.request.post(api+'/records/search', {headers,data:filter});
    assert(response.status()===200, 'HTTP 边界记录查询失败');
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
    await frame.locator('#open-settings').click();
    await frame.locator('#max-text-bytes').fill(String(limit));
    const saved = page.waitForResponse(r => r.url().endsWith('/request-input-audit/settings') && r.request().method()==='POST');
    await frame.locator('#save-settings').click();
    assert((await saved).status()===200, 'HTTP 正文上限未保存');
    await frame.locator('#message').filter({hasText:'正文上限已保存'}).waitFor();
    await displayedLimit(limit);
    assert((await read('/settings')).max_text_bytes===limit, '页面上限与公开配置不一致');
    await frame.locator('#close-management').click();
  }
  /** 从页面刷新并检查持久设置 limit，成功无返回值；查询或显示超时抛错。 */
  async function refreshLimit(limit) {
    const refreshed = page.waitForResponse(r => r.url().endsWith('/request-input-audit/settings') && r.request().method()==='GET');
    await frame.getByRole('button', {name:'刷新已保存记录',exact:true}).click();
    assert((await refreshed).status()===200, 'HTTP 刷新设置失败');
    await displayedLimit(limit);
    assert((await read('/settings')).max_text_bytes===limit, '刷新丢失正文上限');
  }
  /** 按宿主既有 key query 建立真实本地连接，返回 WebSocket；升级失败或超时抛错。 */
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
  /** 在 socket 提交 input 并等待本次公开完成事件及合成回复；成功无返回值，失败、断开或超时抛错。 */
  async function submitWebSocket(socket, input) {
    assert(socket.readyState===WebSocket.OPEN, 'WebSocket 提交前连接已断开');
    await new Promise((resolve, reject) => {
      // 真实模型完成事件证明成功执行，不能把没有记录或仅升级成功当作负例通过。
      const timer = setTimeout(() => finish(new Error('WebSocket 本轮模型没有完成')), 10000);
      const failed = () => finish(new Error('WebSocket 本轮模型连接失败'));
      /** 核对公开 message 的终态与回复；解析或模型失败拒绝等待，其余事件继续等待，不保存原帧。 */
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
  /** 提交 content 到真实 HTTP Chat 执行入口；无返回值，模型执行失败抛错。 */
  async function submitHTTP(content) {
    const response=await page.request.post(origin+'/v1/chat/completions',{headers:{Authorization:'Bearer synthetic-client-audit-a-only'},data:{model:'audit-test-model',messages:[{role:'user',content}]}});
    assert(response.status()===200,'HTTP 控制模型执行失败');
  }
  /** 等待 fixture 的 HTTP 记录并核对正文、协议和状态；suffix 区分截图，返回详情，超时或不一致抛错。 */
  async function openSavedDetail(fixture, suffix) {
    let record;
    const deadline = Date.now()+10000; // 给本地异步 fsync 十秒，只使用本轮标记而非历史条数。
    while (!record && Date.now()<deadline) {
      const list = await search({q:fixture.marker,limit:100});
      record = list.records.find(r => r.preview.startsWith(fixture.marker));
    }
    assert(record && !recordIds.has(record.id), 'HTTP 本轮提交没有独立已保存记录');
    recordIds.add(record.id);
    assert(record.protocol==='chat-completions' && record.truncated===fixture.truncated && record.extraction_incomplete===fixture.incomplete, 'HTTP 本轮列表协议或状态错误');
    await frame.getByRole('button', {name:'刷新已保存记录',exact:true}).click();
    const row = frame.locator('#records tbody tr').filter({hasText:fixture.marker});
    await row.getByRole('cell', {name:'Chat Completions',exact:true}).waitFor();
    // 用途徽标与内容状态独立显示，保留对每个原有状态标签的精确断言。
    for (const label of fixture.label.split(' · ')) await row.locator('.flags').getByText(label,{exact:true}).waitFor();
    const detailResponse = page.waitForResponse(r => r.url().includes('/request-input-audit/detail?') && new URL(r.url()).searchParams.get('id')===record.id);
    await row.getByRole('button', {name:'查看',exact:true}).click();
    const response = await detailResponse;
    assert(response.status()===200, 'HTTP 本轮详情查询失败');
    const detail = await response.json();
    assert(detail.id===record.id && detail.protocol==='chat-completions' && detail.model==='audit-test-model' && detail.text===fixture.want && detail.truncated===fixture.truncated && detail.extraction_incomplete===fixture.incomplete, 'HTTP 列表与详情不对应本轮提交');
    await frame.locator('#detail-meta').filter({hasText:'Chat Completions · audit-test-model · '+fixture.label}).waitFor();
    assert((await frame.locator('#detail').textContent())===fixture.want, 'HTTP 页面正文不是本轮已保存文本');
    if (options.screenshot) {
      const screenshot = suffix ? options.screenshot.replace(/\.png$/, '-'+suffix+'.png') : options.screenshot;
      await frame.locator('#detail-meta').screenshot({path:screenshot});
      if (suffix) extraScreenshots.push(require('node:path').basename(screenshot));
    }
    await frame.locator('#close-detail').click();
    return detail;
  }
  await displayedLimit(65536);
  const status=await read('/status');
  assert(status.available && status.capture_mode==='http_execution' && status.responses_websocket===false && !status.supported_protocols.includes('responses-websocket'),'HTTP 精简版不能声明支持 WebSocket 采集');
  const notice=await frame.locator('#notice').textContent();
  assert(/WebSocket[^。；]*不(支持|采集)|不(支持|采集)[^。；]*WebSocket/.test(notice),'页面没有明确说明 WebSocket 不采集');
  const plain=marker+'HTTP-CONTROL';
  await submitHTTP(plain);
  const original=await openSavedDetail({marker:plain,want:plain,truncated:false,incomplete:false,label:'已保存允许内容'});
  const before=await read('/status');
  const beforeList=await search({limit:100});
  const socket=await connect();
  try {
    for(const suffix of ['WS-FIRST','WS-FOLLOWUP']) {
      await submitWebSocket(socket,[{role:'user',content:[{type:'input_text',text:marker+suffix}]}]);
    }
  } finally {socket.close();}
  const after=await read('/status');
  const afterList=await search({limit:100});
  assert(after.state.saved===before.state.saved && after.state.queued===0 && afterList.total===beforeList.total && JSON.stringify(afterList.records.map(record=>record.id))===JSON.stringify(beforeList.records.map(record=>record.id)),'真实 WebSocket 成功后列表或计数增加');
  assert(JSON.stringify(after.state.gaps)===JSON.stringify(before.state.gaps) && (await search({q:marker+'WS-'})).total===0,'不支持的 WebSocket 被采集或误报缺口');
  // 后续 HTTP 落盘作为处理次序检查，防止负例只在异步写入前查询而假通过。
  const barrier=marker+'HTTP-AFTER-WS';
  await submitHTTP(barrier);
  await openSavedDetail({marker:barrier,want:barrier,truncated:false,incomplete:false,label:'已保存允许内容'},'after-ws');
  assert((await search({limit:100})).total===beforeList.total+1 && (await read('/status')).state.saved===before.state.saved+1 && (await search({q:marker+'WS-'})).total===0,'后续 HTTP 保存后出现 WebSocket 延迟副本');
  const unknown={type:'future',payload:'FORBIDDEN_BROWSER_HTTP_UNKNOWN'};
  const incomplete=marker+'INCOMPLETE';
  await submitHTTP([{type:'text',text:incomplete},{type:'image_url',image_url:{url:'FORBIDDEN_BROWSER_HTTP_IMAGE'}},unknown]);
  await openSavedDetail({marker:incomplete,want:incomplete+'[图片]',truncated:false,incomplete:true,label:'提取不完整'},'incomplete');
  try {
    await saveLimit(7);
    await refreshLimit(7);
    for(const invalid of ['0','1.5']) {
      await frame.locator('#open-settings').click();
      await frame.locator('#max-text-bytes').fill(invalid);
      await frame.locator('#save-settings').click();
      await frame.locator('#message').filter({hasText:'正文上限必须是 1–65536 之间的整数'}).waitFor();
      assert((await read('/settings')).max_text_bytes===7,'页面非法上限改变了旧值');
      const rejected=await page.request.post(api+'/settings',{headers,data:{max_text_bytes:Number(invalid)}});
      assert(rejected.status()===400 && (await read('/settings')).max_text_bytes===7,'公开接口没有拒绝非法上限并保留旧值');
      await frame.locator('#close-management').click();
      await refreshLimit(7);
    }
    for(const [i,incomplete] of [false,true].entries()) {
      // 七个 ASCII 字节作为本轮独立标记；之后的四字节字符不能被切断。
      const shortMarker=require('node:crypto').randomUUID().replace(/-/g,'').slice(0,7);
      const content=[{type:'text',text:shortMarker+'🙂FORBIDDEN_BROWSER_HTTP_TAIL'}];
      if(incomplete)content.push(unknown);
      await submitHTTP(content);
      await openSavedDetail({marker:shortMarker,want:shortMarker,truncated:true,incomplete,label:incomplete?'已截断 · 提取不完整':'已截断'},i===0?'truncated':'combined');
    }
    const preserved=await read('/detail?id='+encodeURIComponent(original.id));
    assert(preserved.text===plain && !preserved.truncated,'修改上限追溯改变了原有 HTTP 记录');
    await saveLimit(65536);
    await refreshLimit(65536);
    const restored=marker+'RESTORED';
    await submitHTTP(restored);
    await openSavedDetail({marker:restored,want:restored,truncated:false,incomplete:false,label:'已保存允许内容'},'restored');
  } finally {
    const restored=await page.request.post(api+'/settings',{headers,data:{max_text_bytes:65536}});
    assert(restored.status()===200,'HTTP 边界场景未恢复正文上限');
  }
  return {target:'websocket-not-captured',extraScreenshots,websocketModelCompleted:true,websocketSubmissions:2,websocketRecords:0,unchangedListAndCounters:true,noWebsocketGap:true,httpControlRecords:recordIds.size,unsupportedNotice:true,reducedMaxTextBytes:7,refreshPreservedLimit:true,invalidLimitsRejected:true,priorRecordPreserved:true,restoredMaxTextBytes:65536,sameConnection:true};
};
