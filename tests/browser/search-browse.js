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
  /** 以 JSON 提交 filter 查询已保存记录；返回 JSON，HTTP 失败抛错，不将关键词写入 URL。 */
  async function search(filter) {
    const response=await page.request.post(api+'/records/search',{headers,data:filter});
    assert(response.status()===200,'搜索验收记录查询失败');
    return response.json();
  }
  const objects = (await read('/objects')).objects;
  for (const object of objects) {
    const response = await page.request.post(api+'/monitor',{headers,data:{object_id:object.id,name}});
    assert(response.status()===200,'搜索验收监控配置失败');
  }
  /** 提交本轮文本；key/model 指定身份与模型，content 是消息，purposeHeaders/reasoning 是合成请求设置；异常状态抛错，无返回值。 */
  async function submit(key, model, content, purposeHeaders={}, reasoning={}) {
    const response = await page.request.post(origin+'/v1/chat/completions',{headers:{Authorization:'Bearer '+key,...purposeHeaders},data:{model,messages:[{role:'user',content}],...reasoning}});
    assert(response.status()===200 || response.status()===400,'搜索验收模型提交失败');
  }
  const clientA='synthetic-client-audit-a-only', clientB='synthetic-client-audit-b-only';
  // 复用原分页样本验证用途，不增加场景或改变25条计数；这些是合成 HTTP 头，不是实际客户端发送证据。
  for (let i=0;i<22;i++) {
    const purposeHeaders=i===0 ? {'x-claude-code-request-class':'main'} : i===1 ? {'x-codex-turn-metadata':'{"request_kind":"turn"}'} : {};
    await submit(clientA,'audit-test-model',marker+'ENTRY-'+String(i).padStart(2,'0'),purposeHeaders,i===0?{reasoning_effort:'high'}:{});
  }
  const prefix=marker+'ENTRY-22 '+hostile;
  const text=prefix+'x'.repeat(65535-Buffer.byteLength(prefix));
  await submit(clientA,'audit-test-model',[{type:'text',text:text+'🙂FORBIDDEN_SEARCH_BROWSER_TAIL'},{type:'future',payload:'FORBIDDEN_SEARCH_BROWSER_UNKNOWN'}]);
  await submit(clientB,'audit-test-model',marker+'OTHER-OBJECT');
  await submit(clientA,'audit-test-other-model',marker+'OTHER-MODEL');
  const hostileModel='<img src="https://invalid.example/search-model">';
  await submit(clientA,hostileModel,marker+'UNROUTED-MODEL');
  let saved;
  const deadline=Date.now()+10000;
  while (Date.now()<deadline) {
    saved=await search({limit:100,q:marker});
    if (saved.total===25) break;
  }
  // 第二个可路由模型保留模型交集验证；未知模型在执行前被拒绝，不能记录其正文。
  assert(saved.total===25 && !saved.records.some(record=>record.preview===marker+'UNROUTED-MODEL'),'本轮 HTTP 执行夹具未保存或误采路由失败正文');
  const target=saved.records.find(record=>record.preview.startsWith(marker+'ENTRY-22'));
  assert(target && target.truncated && target.extraction_incomplete && target.purpose==='unknown','搜索夹具双状态或未知用途丢失');
  const targetDetail=await read('/detail?id='+encodeURIComponent(target.id));
  assert(targetDetail.id===target.id && targetDetail.purpose===target.purpose,'未知用途列表与详情不一致');
  await frame.getByRole('button',{name:'刷新已保存记录',exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('iframe[title="输入审计"]').contentDocument.querySelector('#refresh').disabled);
  const availableModels=(await read('/models')).models;
  assert(availableModels.includes('audit-test-model') && availableModels.includes('audit-test-other-model') && JSON.stringify(availableModels)===JSON.stringify([...new Set(availableModels)].sort()),'模型集合不是全部已保存模型的排序去重结果');
  assert(JSON.stringify(await frame.locator('#model-filter option').evaluateAll(options=>options.map(option=>option.value)))===JSON.stringify(['',...availableModels]),'模型下拉选项没有保留原始模型名与全部模型空值');
  const external=[];
  const onRequest=request=>{if(new URL(request.url()).origin!==origin)external.push(true);};
  page.on('request',onRequest);
  /** 点击 selector 查询（默认应用筛选）；返回公开结果并等待刷新完成，失败抛错，不保存数据。 */
  async function apply(selector='#apply-filters') {
    const pending=page.waitForResponse(r=>r.url()===api+'/records/search'&&r.request().method()==='POST');
    await frame.locator(selector).click();
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
  /** 核对当前唯一结果的用途 API、列表标签与详情；purpose/label 是独立预期，失败抛错，成功无返回值。 */
  async function verifyPurpose(result, purpose, label) {
    assert(result.total===1 && result.records.length===1 && result.records[0].purpose===purpose,'用途列表分类错误');
    const record=result.records[0];
    const detail=await read('/detail?id='+encodeURIComponent(record.id));
    assert(detail.id===record.id && detail.purpose===purpose,'用途详情与列表不一致');
    const expectedReasoning=purpose==='main'?{effort:'high'}:{};
    assert(JSON.stringify(record.request_reasoning)===JSON.stringify(expectedReasoning) && JSON.stringify(detail.request_reasoning)===JSON.stringify(expectedReasoning),'请求思考字段列表与详情不一致');
    const purposeRow=frame.locator('#records tbody tr').filter({hasText:record.preview});
    await purposeRow.locator('.reasoning').filter({hasText:purpose==='main'?'effort=high':'未指定'}).waitFor();
    await purposeRow.locator('.flags .badge').filter({hasText:label}).waitFor();
    await purposeRow.getByRole('button',{name:'查看',exact:true}).click();
    await frame.locator('#detail-meta').filter({hasText:label}).waitFor();
    await page.waitForFunction(expected=>document.querySelector('iframe[title="输入审计"]').contentDocument.querySelector('#detail').textContent===expected,detail.text);
    await frame.locator('#close-detail').click();
  }
  try {
    await frame.getByRole('combobox',{name:'历史对象筛选',exact:true}).selectOption(target.object_id);
    /** 将 epoch 转为本地秒精度输入；范围已有五秒余量，避免 Chromium 去掉毫秒末尾零使 Playwright fill 误报格式错误。 */
    const localTime=ms=>page.evaluate(value=>{const date=new Date(value);return new Date(value-date.getTimezoneOffset()*60000).toISOString().slice(0,19);},ms);
    await frame.locator('#from-filter').fill(await localTime(started));
    await frame.locator('#to-filter').fill(await localTime(Date.now()+5000));
    await frame.getByRole('combobox',{name:'执行输入模型筛选',exact:true}).selectOption('audit-test-model');
    await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill(marker);
    let result=await apply();
    assert(result.total===23 && result.records.length===20,'四条件交集或第一页错误');
    assert(JSON.stringify(await frame.locator('#model-filter option').evaluateAll(options=>options.map(option=>option.value)))===JSON.stringify(['',...availableModels]),'模型选项随关键词、分页或对象条件缩减');
    await frame.getByRole('combobox',{name:'执行输入模型筛选',exact:true}).selectOption('audit-test-other-model');
    result=await apply('#refresh');
    assert(result.total===23 && await frame.locator('#model-filter').inputValue()==='audit-test-other-model','刷新丢失模型草稿或把草稿当作已应用条件');
    let modelMode='missing';
    /** 模拟模型过期与加载失败；route 为模型请求，无返回值，处理失败抛错，不改变记录查询。 */
    const modelOptions=route=>route.fulfill({status:modelMode==='failure'?503:200,json:modelMode==='failure'?{error:'synthetic_models_failure'}:{models:availableModels.filter(model=>model!=='audit-test-model')}});
    await page.route(api+'/models',modelOptions);
    try {
      result=await apply('#refresh');
      assert(result.total===23 && await frame.locator('#model-filter').inputValue()==='audit-test-other-model','已应用模型消失时被悄悄改为全部或覆盖草稿');
      await frame.locator('#model-filter option[value="audit-test-model"]').filter({hasText:'当前无记录'}).waitFor({state:'attached'});
      modelMode='failure';result=await apply('#refresh');
      await frame.locator('#models-state').filter({hasText:'模型加载失败'}).filter({hasText:'当前模型集合未知'}).waitFor();
      assert(result.total===23 && await frame.locator('#model-filter').inputValue()==='audit-test-other-model','模型加载失败时丢失已应用条件或草稿');
    } finally {await page.unroute(api+'/models',modelOptions);}
    await frame.getByRole('combobox',{name:'执行输入模型筛选',exact:true}).selectOption('audit-test-model');
    await frame.locator('#page').filter({hasText:'第 1 页；匹配 23 条'}).waitFor();
    const row=frame.locator('#records tbody tr').filter({hasText:marker+'ENTRY-22'});
    await row.locator('.flags .badge').filter({hasText:'已截断'}).waitFor();
    await row.locator('.flags .badge').filter({hasText:'提取不完整'}).waitFor();
    await row.locator('.flags .badge').filter({hasText:'用途未知'}).waitFor();
    await row.locator('.object-name').waitFor();
    assert(await row.locator('.object-name').textContent()===name,'搜索结果未精确显示纯文本对象名称');
    await row.getByRole('button',{name:'查看',exact:true}).click();
    await frame.locator('#detail-meta').filter({hasText:'已截断 · 提取不完整'}).filter({hasText:'用途未知'}).waitFor();
    const visible=await frame.locator('#detail').evaluate(element=>({text:element.textContent,bytes:new TextEncoder().encode(element.textContent).length,nodes:element.querySelectorAll('img,a,script,style').length,injected:element.ownerDocument.defaultView.__searchInjected===true}));
    assert(visible.text===text && visible.bytes===65535 && visible.nodes===0 && !visible.injected,'搜索详情没有保持已保存纯文本');
    assert(await frame.locator('#records b,#records img,#records script,#detail-meta b').count()===0,'名称或预览被解析为HTML');
    await frame.locator('#close-detail').click();
    await row.locator('.flags').screenshot({path:options.screenshot});
    const next=page.waitForResponse(r=>r.url()===api+'/records/search'&&r.request().method()==='POST'&&r.request().postDataJSON().offset===20);
    await frame.getByRole('button',{name:'下一页',exact:true}).click();
    const nextResponse=await next;
    const query=nextResponse.request().postDataJSON();
    result=await nextResponse.json();
    assert(result.total===23 && result.records.length===3 && query.q===marker && query.object_id===target.object_id && query.model==='audit-test-model' && query.from && query.to && query.offset===20 && query.limit===20,'翻页丢失已应用 JSON 条件');
    assert(new URL(nextResponse.url()).search==='', '搜索条件不得进入 URL');
    await frame.locator('#page').filter({hasText:'第 2 页；匹配 23 条'}).waitFor();
    await frame.getByRole('button',{name:'上一页',exact:true}).click();
    await frame.locator('#page').filter({hasText:'第 1 页；匹配 23 条'}).waitFor();
    await frame.getByRole('button',{name:'下一页',exact:true}).click();
    await frame.locator('#page').filter({hasText:'第 2 页；匹配 23 条'}).waitFor();
    await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill(marker+'ENTRY-00');
    result=await apply();
    assert(result.total===1 && result.offset===0,'应用新筛选没有返回首页');
    await verifyPurpose(result,'main','主会话调用');
    await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill(marker+'ENTRY-01');
    result=await apply();
    await verifyPurpose(result,'turn','对话轮次');
    await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill('FORBIDDEN_SEARCH_BROWSER_TAIL');
    result=await apply();
    assert(result.total===0,'未保存尾部被页面搜索命中');
    await frame.locator('#empty').waitFor();
    const cleared=page.waitForResponse(r=>r.url()===api+'/records/search'&&r.request().method()==='POST'&&!r.request().postDataJSON().q);
    await frame.getByRole('button',{name:'清空筛选',exact:true}).click();
    assert((await cleared).status()===200,'清空筛选失败');
    await frame.locator('#records tbody tr').filter({hasText:marker+'OTHER-MODEL'}).getByRole('cell',{name:'audit-test-other-model',exact:true}).waitFor();
    assert(await frame.locator('#records img,#records script,#records style').count()===0,'搜索结果被解释为HTML');
    assert(external.length===0,'搜索内容触发外部请求');
  } finally {page.off('request',onRequest);}
  const layout=await verifyLayout(page,{keyword:marker,recordId:target.id,text});
  // 独立标记在原25条分页和布局验收后提交，避免新增样本改变已约定计数。
  const clientMarker='BROWSER-CLIENT-FILTER-'+Date.now()+'-';
  const real=clientMarker+'REAL 正常正文';
  const skill='Base directory for this skill: /synthetic/path\n\n'+clientMarker+'AUX-SKILL\nFollow this workflow.';
  const reminder='<system-reminder>\n'+clientMarker+'AUX-REMINDER\n</system-reminder>';
  const tool={type:'tool_result',tool_use_id:'synthetic-browser-tool',content:clientMarker+'AUX-TOOL'};
  /** 经真实 Messages 路由提交末尾 content；失败抛固定错误，成功无返回值，不把样本写入 URL。 */
  async function submitAnthropic(content) {
    const response=await page.request.post(origin+'/v1/messages',{
      headers:{Authorization:'Bearer '+clientA},
      data:{model:'audit-test-model',max_tokens:32,messages:[{role:'user',content:clientMarker+'AUX-HISTORY'},{role:'user',content}]}
    });
    assert(response.status()===200,'客户端过滤浏览器 Messages 提交失败');
  }
  for (const content of [
    '[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]\n\nFIRST: Look at the user\'s recent messages and original request.\n\nYour job is to predict what THEY would type - not what you think they should do.\n\n'+clientMarker+'AUX-SUGGESTION\nFormat: 2-12 words, match the user\'s style. Or nothing.\n\nReply with ONLY the suggestion, no quotes or explanation.',
    'Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools.\n\nGood: Updating parser.go tests\nBad (past tense): Updated parser.go tests\n\n'+clientMarker+'AUX-ACTION',
    '<session>\n'+clientMarker+'AUX-SESSION\n</session>\n\nWrite the title in Chinese. Keep technical terms and code identifiers in their original form.',
    '<transcript>\n{"user":"'+clientMarker+'AUX-TRANSCRIPT"}{"Agent":"'+clientMarker+'AUX-AGENT"}\n</transcript>\n附加说明：判断工具调用权限。',
    [tool,{type:'text',text:skill}],
    [{type:'text',text:reminder}]
  ]) await submitAnthropic(content);
  await submitAnthropic([
    tool,{type:'text',text:skill},{type:'text',text:reminder},
    {type:'text',text:'<ide_opened_file>\nThe user opened /synthetic/'+clientMarker+'AUX-IDE.go in the IDE.\n</ide_opened_file>'},
    {type:'text',text:real}
  ]);
  // 最后正常正文的保存是同队列FIFO屏障，不用固定等待推断辅助请求已跳过。
  let filtered;
  const clientDeadline=Date.now()+10000;
  while (Date.now()<clientDeadline) {
    filtered=await search({q:real,limit:100});
    if (filtered.total===1) break;
  }
  assert(filtered.total===1,'客户端过滤的本轮正常正文未保存');
  filtered=await search({q:clientMarker,limit:100});
  assert(filtered.total===1 && filtered.records.length===1 && filtered.records[0].protocol==='anthropic-messages' && !filtered.records[0].truncated && !filtered.records[0].extraction_incomplete,'辅助请求被保存或正常正文状态错误');
  const filteredDetail=await read('/detail?id='+encodeURIComponent(filtered.records[0].id));
  assert(filteredDetail.text===real && filtered.records[0].purpose==='unknown' && filteredDetail.purpose==='unknown','过滤后的 API 详情含客户端附加内容或未知用途不一致');
  assert((await search({q:clientMarker+'AUX-'})).total===0,'客户端辅助内容仍可经 API 检索');
  const resetClientFilters=page.waitForResponse(r=>r.url()===api+'/records/search'&&r.request().method()==='POST'&&!r.request().postDataJSON().q);
  await frame.getByRole('button',{name:'清空筛选',exact:true}).click();
  assert((await resetClientFilters).status()===200,'客户端过滤页面清空条件失败');
  await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill(clientMarker);
  let clientResult=await apply();
  assert(clientResult.total===1 && clientResult.records[0].id===filtered.records[0].id,'页面搜索未仅显示正常正文');
  const clientRow=frame.locator('#records tbody tr').filter({hasText:real});
  await clientRow.getByRole('cell',{name:'Anthropic Messages',exact:true}).waitFor();
  await clientRow.locator('.flags .badge').filter({hasText:'用途未知'}).waitFor();
  await clientRow.getByRole('button',{name:'查看',exact:true}).click();
  await page.waitForFunction(expected=>document.querySelector('iframe[title="输入审计"]').contentDocument.querySelector('#detail').textContent===expected,real);
  assert(await frame.locator('#detail').textContent()===real,'页面详情未精确保留正常正文');
  await frame.locator('#detail-meta').filter({hasText:'用途未知'}).waitFor();
  await frame.locator('#close-detail').click();
  await frame.getByRole('textbox',{name:'正文关键词',exact:true}).fill(clientMarker+'AUX-');
  clientResult=await apply();
  assert(clientResult.total===0,'页面仍能搜索客户端辅助关键词');
  await frame.locator('#empty').waitFor();
  return {intersection:true,pagination:true,filterReset:true,singleDetail:true,contentFlags:true,plainText:true,clientInputFiltering:true,requestPurpose:true,externalRequests:0,layout};
};
