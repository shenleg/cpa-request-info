// 本文件通过真实宿主管理页、插件菜单及同源受保护接口验收；仅使用合成 Key，不保存认证材料。
// 场景由独立 runner 导入，page 已导航到本轮合成实例；成功返回有限证据，验收失败抛错。
/** 验证 page 所在合成宿主的菜单、对象配置与正文安全；返回有限证据，鉴权或行为不符时抛错。 */
module.exports = async (page) => {
  /** 验证 condition；失败时以 message 抛出错误，成功无返回值，不输出凭据或正文。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const admin = 'synthetic-admin-audit-test-only';
  const base = new URL(page.url()).origin;
  const clientKeys = ['synthetic-client-audit-a-only', 'synthetic-client-audit-b-only'];
  const marker = 'BROWSER-SMOKE-' + Date.now() + '-';
  const focusMarker=marker+'FOCUS-';
  const text = focusMarker + '<script>window.__auditInjected=true</script>\n<img src="https://example.invalid/audit-forbidden-image">\n![图片](https://example.invalid/audit-forbidden-markdown)';
  const external = [];
  page.on('request', request => { if (request.url().includes('audit-forbidden')) external.push(request.url()); });
  await page.goto(base + '/management.html');
  await page.waitForFunction(() => document.querySelector('input[type="password"]') || document.querySelector('a[href="#/plugin-pages/request-input-audit/0"]'));
  if (await page.getByRole('textbox', {name:'管理密钥:'}).count()) {
    await page.getByRole('textbox', {name:'管理密钥:'}).fill(admin);
    await page.getByRole('checkbox', {name:'记住密码'}).uncheck();
    await page.getByRole('button', {name:'登录', exact:true}).click();
  }
  await page.getByRole('link', {name:'输入审计',exact:true}).click();
  const frame = page.frameLocator('iframe[title="输入审计"]');
  await frame.getByRole('textbox', {name:'临时管理 Key'}).fill(admin);
  await frame.getByRole('button', {name:'连接',exact:true}).click();
  await frame.getByRole('status').filter({hasText:'管理鉴权通过'}).waitFor();
  const api=base+'/v0/management/plugins/request-input-audit';
  const headers={Authorization:'Bearer '+admin};
  const statusResponse=await page.request.get(api+'/status',{headers});
  assert(statusResponse.status()===200,'HTTP 执行状态不可用');
  const status=await statusResponse.json();
  assert(status.available && status.capture_mode==='http_execution' && status.responses_websocket===false && JSON.stringify([...status.supported_protocols].sort())===JSON.stringify(['anthropic-messages','chat-completions','responses']),'HTTP 精简版能力声明错误');
  const catalogResponse=await page.request.get(api+'/objects',{headers});
  assert(catalogResponse.status()===200 && (await catalogResponse.json()).catalog==='observed','目录必须来自已观察对象而非宿主 Key 枚举');
  const discovery=await page.request.post(api+'/records/search',{headers,data:{q:'synthetic HTTP discovery only'}});
  assert(discovery.status()===200 && (await discovery.json()).total===0,'首次发现未监控对象不应保存正文，重启不能重采发现请求');
  const ids = await frame.locator('#objects option').evaluateAll(options => options.map(option => option.value));
  assert(ids.length === 2, 'demo 应先用两个合成 Key 完成 HTTP 发现');
  await frame.locator('#open-objects').click();
  for (const [i,id] of ids.entries()) {
    await frame.locator('#objects').selectOption(id);
    await frame.getByRole('textbox',{name:'名称',exact:true}).fill('浏览器合成对象' + i);
    const response = page.waitForResponse(r => r.url().endsWith('/request-input-audit/monitor') && r.request().method() === 'POST');
    await frame.getByRole('button',{name:'选择并命名'}).click();
    assert((await response).status() === 200, '监控配置未保存');
    await frame.locator('#objects option').filter({hasText:'浏览器合成对象'+i}).waitFor({state:'attached'});
  }
  await frame.locator('#close-management').click();
  for (const [i,key] of clientKeys.entries()) {
    const input=i===clientKeys.length-1?text:marker+'OTHER-<script>window.__auditInjected=true</script>';
    const result = await page.request.post(base+'/v1/chat/completions',{headers:{Authorization:'Bearer '+key},data:{model:'audit-test-model',messages:[{role:'system',content:'FORBIDDEN_BROWSER_SYSTEM'},{role:'user',content:'FORBIDDEN_BROWSER_HISTORY'},{role:'user',content:input}]}});
    assert(result.status() === 200,'实际可控模型应成功');
  }
  // 从受保护结果确认本轮落盘，再定位本轮标记，不能把旧记录当作新的结果。
  let savedId='';
  const deadline=Date.now()+10000;
  while (!savedId && Date.now()<deadline) {
    const response=await page.request.post(base+'/v0/management/plugins/request-input-audit/records/search',{headers:{Authorization:'Bearer '+admin},data:{q:focusMarker,limit:100}});
    assert(response.status()===200,'受保护结果查询失败');
    const list=await response.json();
    const record=list.records.find(r=>r.preview.startsWith(focusMarker));
    if(record)savedId=record.id;
  }
  assert(savedId!=='','本轮输入没有落盘');
  await frame.getByRole('button',{name:'刷新已保存记录'}).click();
  const row=frame.locator('#records tbody tr').filter({hasText:focusMarker}).first();
  await row.waitFor();
  const detailResponse=page.waitForResponse(r=>r.url().includes('/request-input-audit/detail?')&&new URL(r.url()).searchParams.get('id')===savedId);
  await row.getByRole('button',{name:'查看',exact:true}).click();
  assert((await detailResponse).status()===200,'本轮详情查询失败');
  await frame.locator('#detail').filter({hasText:focusMarker}).waitFor();
  const result = await frame.locator('body').evaluate(body => {
    const w = body.ownerDocument.defaultView;
    return {injected:w.__auditInjected === true,images:body.querySelectorAll('#detail img,#records img').length,text:body.querySelector('#detail').textContent,keyFieldEmpty:body.querySelector('#management-key').value==='',pluginStorage:Object.keys(w.localStorage).filter(k=>k.includes('audit'))};
  });
  assert(result.text === text && !result.injected && result.images === 0,'正文必须按字面纯文本显示');
  assert(result.keyFieldEmpty && result.pluginStorage.length === 0,'不得新增持久化管理 Key');
  assert(external.length === 0,'不能自动加载输入中的外部资源');
  await frame.locator('#close-detail').click();
  return {menu:true,selection:true,details:true,plainText:true,externalRequests:0,keyStoredByPlugin:false};
}
