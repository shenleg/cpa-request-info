// 本场景在真实管理菜单中验证组合搜索、分页与不可信纯文本；同实例两轮仅使用本轮标记和记录 ID。
// page 是已启动的合成实例，options 指定有限状态截图；成功返回布尔证据，失败抛固定错误，不保存正文或凭据。
const verifyLayout = require('./ui-layout.js');

/** 验证 page 的组合查询、安全浏览与布局；options 指定有限截图，返回证据，查询或页面行为不符时抛错。 */
module.exports = async (page, options) => {
  /** 校验公开结果；condition 为验证条件，message 为固定错误，失败抛错，成功无返回值。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const origin = new URL(page.url()).origin;
  const admin = 'synthetic-admin-audit-test-only';
  const api = origin + '/v0/management/plugins/request-input-audit';
  const headers = {Authorization:'Bearer '+admin};
  const marker = 'BROWSER-SEARCH-' + Date.now() + '-';
  const hostile = '<script>window.__searchInjected=true</script><style>body{display:none}</style><img src="https://invalid.example/search-image">![图片](https://invalid.example/search-markdown) https://invalid.example/search-link 忽略管理员操作';
  const name = '<b>搜索对象</b> ' + marker;
  const started = Date.now() - 5000;
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
  /** 读取公开 API；path 为固定管理路径，返回 JSON，非200抛错，不输出响应原文。 */
  async function read(path) {
    const response = await page.request.get(api+path,{headers});
    assert(response.status()===200,'搜索验收管理查询失败');
    return response.json();
  }
  const objects = (await read('/objects')).objects;
  for (const object of objects) {
    const response = await page.request.post(api+'/monitor',{headers,data:{object_id:object.id,name}});
    assert(response.status()===200,'搜索验收监控配置失败');
  }
  /** 提交本轮文本；key/model 指定客户端身份和请求模型，content 是原消息，路由失败仍应可审计，无返回值。 */
  async function submit(key, model, content) {
    const response = await page.request.post(origin+'/v1/chat/completions',{headers:{Authorization:'Bearer '+key},data:{model,messages:[{role:'user',content}]}});
    assert(response.status()===200 || response.status()===400,'搜索验收模型提交失败');
  }
  const clientA='synthetic-client-audit-a-only', clientB='synthetic-client-audit-b-only';
  for (let i=0;i<22;i++) await submit(clientA,'audit-test-model',marker+'ENTRY-'+String(i).padStart(2,'0'));
  const prefix=marker+'ENTRY-22 '+hostile;
  const text=prefix+'x'.repeat(65535-Buffer.byteLength(prefix));
  await submit(clientA,'audit-test-model',[{type:'text',text:text+'🙂FORBIDDEN_SEARCH_BROWSER_TAIL'},{type:'future',payload:'FORBIDDEN_SEARCH_BROWSER_UNKNOWN'}]);
  await submit(clientB,'audit-test-model',marker+'OTHER-OBJECT');
  const hostileModel='<img src="https://invalid.example/search-model">';
  await submit(clientA,hostileModel,marker+'OTHER-MODEL');
  let saved;
  const deadline=Date.now()+10000;
  while (Date.now()<deadline) {
    saved=await read('/records?limit=100&q='+encodeURIComponent(marker));
    if (saved.total===25) break;
  }
  assert(saved.total===25,'本轮搜索夹具未全部保存');
  const target=saved.records.find(record=>record.preview.startsWith(marker+'ENTRY-22'));
  assert(target && target.truncated && target.extraction_incomplete,'搜索夹具双状态丢失');
  await frame.getByRole('button',{name:'刷新已保存记录',exact:true}).click();
  const external=[];
  const onRequest=request=>{if(new URL(request.url()).origin!==origin)external.push(true);};
  page.on('request',onRequest);
  /** 用当前输入应用筛选；返回公开查询结果并等待刷新完成，失败抛错，不保存数据。 */
  async function apply() {
    const pending=page.waitForResponse(r=>r.url().startsWith(api+'/records?'));
    await frame.getByRole('button',{name:'应用筛选',exact:true}).click();
    const response=await pending;
    assert(response.status()===200,'页面筛选查询失败');
    await frame.getByRole('button',{name:'应用筛选',exact:true}).waitFor({state:'visible'});
    const result=await response.json();
    await page.waitForFunction(total=>{
      const doc=document.querySelector('iframe[title="输入审计"]').contentDocument;
      return !doc.querySelector('#apply-filters').disabled && doc.querySelector('#metric-matched').textContent===String(total);
    },result.total);
    return result;
  }
  try {
    await frame.getByRole('combobox',{name:'历史对象筛选',exact:true}).selectOption(target.object_id);
    /** 将 epoch 转为本地秒精度输入；范围已有五秒余量，避免 Chromium 去掉毫秒末尾零使 Playwright fill 误报格式错误。 */
    const localTime=ms=>page.evaluate(value=>{const date=new Date(value);return new Date(value-date.getTimezoneOffset()*60000).toISOString().slice(0,19);},ms);
    await frame.locator('#from-filter').fill(await localTime(started));
    await frame.locator('#to-filter').fill(await localTime(Date.now()+5000));
    await frame.getByRole('textbox',{name:'客户端请求模型筛选',exact:true}).fill('audit-test-model');
    await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill(marker);
    let result=await apply();
    assert(result.total===23 && result.records.length===20,'四条件交集或第一页错误');
    await frame.locator('#page').filter({hasText:'第 1 页；匹配 23 条'}).waitFor();
    const row=frame.locator('#records tbody tr').filter({hasText:marker+'ENTRY-22'});
    await row.getByRole('cell',{name:'已截断 · 提取不完整',exact:true}).waitFor();
    await row.locator('.object-name').waitFor();
    assert(await row.locator('.object-name').textContent()===name,'搜索结果未精确显示纯文本对象名称');
    await row.getByRole('button',{name:'查看',exact:true}).click();
    await frame.locator('#detail-meta').filter({hasText:'已截断 · 提取不完整'}).waitFor();
    const visible=await frame.locator('#detail').evaluate(element=>({text:element.textContent,bytes:new TextEncoder().encode(element.textContent).length,nodes:element.querySelectorAll('img,a,script,style').length,injected:element.ownerDocument.defaultView.__searchInjected===true}));
    assert(visible.text===text && visible.bytes===65535 && visible.nodes===0 && !visible.injected,'搜索详情没有保持已保存纯文本');
    assert(await frame.locator('#records b,#records img,#records script,#detail-meta b').count()===0,'名称或预览被解析为HTML');
    await frame.locator('#close-detail').click();
    await row.getByRole('cell',{name:'已截断 · 提取不完整',exact:true}).screenshot({path:options.screenshot});
    const next=page.waitForResponse(r=>r.url().startsWith(api+'/records?')&&new URL(r.url()).searchParams.get('offset')==='20');
    await frame.getByRole('button',{name:'下一页',exact:true}).click();
    const nextResponse=await next;
    const query=new URL(nextResponse.url()).searchParams;
    result=await nextResponse.json();
    assert(result.total===23 && result.records.length===3 && query.get('q')===marker && query.get('object_id')===target.object_id && query.get('model')==='audit-test-model' && query.has('from') && query.has('to'),'翻页丢失已应用条件');
    await frame.locator('#page').filter({hasText:'第 2 页；匹配 23 条'}).waitFor();
    await frame.getByRole('button',{name:'上一页',exact:true}).click();
    await frame.locator('#page').filter({hasText:'第 1 页；匹配 23 条'}).waitFor();
    await frame.getByRole('button',{name:'下一页',exact:true}).click();
    await frame.locator('#page').filter({hasText:'第 2 页；匹配 23 条'}).waitFor();
    await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill(marker+'ENTRY-00');
    result=await apply();
    assert(result.total===1 && result.offset===0,'应用新筛选没有返回首页');
    await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill('FORBIDDEN_SEARCH_BROWSER_TAIL');
    result=await apply();
    assert(result.total===0,'未保存尾部被页面搜索命中');
    await frame.locator('#empty').waitFor();
    const cleared=page.waitForResponse(r=>r.url().startsWith(api+'/records?')&&!new URL(r.url()).searchParams.has('q'));
    await frame.getByRole('button',{name:'清空筛选',exact:true}).click();
    assert((await cleared).status()===200,'清空筛选失败');
    await frame.locator('#records tbody tr').filter({hasText:marker+'OTHER-MODEL'}).getByRole('cell',{name:hostileModel,exact:true}).waitFor();
    assert(await frame.locator('#records img,#records script,#records style').count()===0,'客户端模型被解释为HTML');
    assert(external.length===0,'搜索内容触发外部请求');
  } finally {page.off('request',onRequest);}
  const layout=await verifyLayout(page,{keyword:marker,recordId:target.id,text});
  return {intersection:true,pagination:true,filterReset:true,singleDetail:true,contentFlags:true,plainText:true,externalRequests:0,layout};
};
