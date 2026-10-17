// 本文件通过真实宿主管理菜单验证截断、不完整及双状态提示；只用合成凭据，不读私有存储。
// 场景由独立 runner 导入；page 位于合成实例，options 指定本轮截图路径。
// 返回本轮截断/不完整显示与字节边界的有限验收证据；鉴权、落盘或页面校验失败抛错。
/** 验证 page 的协议正文与内容标记；options 指定有限截图，返回本轮证据，保存或展示不符时抛错。 */
module.exports = async (page, options = {}) => {
  /** 验证 condition 的用户可见结果；失败以 message 抛错，成功无返回，不输出凭据或超长正文。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const origin = new URL(page.url()).origin;
  const admin = 'synthetic-admin-audit-test-only';
  const api = origin + '/v0/management/plugins/request-input-audit';
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
  await frame.locator('#open-objects').click();
  for (const [i,id] of ids.entries()) {
    await frame.locator('#objects').selectOption(id);
    await frame.getByRole('textbox', {name:'名称',exact:true}).fill('超长验收对象' + i);
    const saved = page.waitForResponse(r => r.url().endsWith('/request-input-audit/monitor') && r.request().method()==='POST');
    await frame.getByRole('button', {name:'选择并命名'}).click();
    assert((await saved).status()===200, '监控选择未保存');
    await frame.locator('#objects option').filter({hasText:'超长验收对象'+i}).waitFor({state:'attached'});
  }
  await frame.locator('#close-management').click();
  /** 等待 fixture.marker 的本轮落盘并打开精确详情；fixture 指定两个标记及可见标签。
   * 返回已核对详情；HTTP、状态或等待失败抛错，不通过旧记录证明本轮成功。 */
  async function openSavedDetail(fixture) {
    let id = '';
    // 给真实 fsync 和管理鉴权查询十秒冷启动余量，不用固定 sleep 假定已经落盘。
    const deadline = Date.now() + 10000;
    while (!id && Date.now()<deadline) {
      const response = await page.request.post(api + '/records/search', {headers:{Authorization:'Bearer '+admin},data:{q:fixture.marker,limit:100}});
      assert(response.status()===200, '受保护记录查询失败');
      const list = await response.json();
      const record = list.records.find(r => r.preview.startsWith(fixture.marker) && r.truncated===fixture.truncated && r.extraction_incomplete===fixture.incomplete);
      if (record) id = record.id;
    }
    assert(id !== '', '本轮符合状态的记录没有实际保存');
    await frame.getByRole('button', {name:'刷新已保存记录'}).click();
    const row = frame.locator('#records tbody tr').filter({hasText:fixture.marker});
    // 内容状态与用途是独立标签，逐项精确核对，不能把整个状态单元格当成单一标签。
    for (const label of fixture.label.split(' · ')) await row.locator('.flags').getByText(label,{exact:true}).waitFor();
    const detailResponse = page.waitForResponse(r => r.url().includes('/request-input-audit/detail?') && new URL(r.url()).searchParams.get('id')===id);
    await row.getByRole('button', {name:'查看',exact:true}).click();
    const response = await detailResponse;
    assert(response.status()===200, '本轮详情查询失败');
    const detail = await response.json();
    assert(detail.id===id && detail.truncated===fixture.truncated && detail.extraction_incomplete===fixture.incomplete, '本轮列表/详情状态不一致');
    await frame.locator('#detail-meta').filter({hasText:fixture.label}).waitFor();
    await frame.locator('#detail').filter({hasText:fixture.marker}).waitFor();
    return detail;
  }
  // 65535 个 ASCII 后放一个四字节字符，64KiB 前缀必须停在字符之前，不能切断 UTF-8。
  const prefix = 'BROWSER-LIMIT-' + Date.now() + '-';
  const input = prefix + 'x'.repeat(65535 - prefix.length) + '🙂FORBIDDEN_BROWSER_TAIL';
  const result = await page.request.post(origin + '/v1/chat/completions', {
    headers:{Authorization:'Bearer synthetic-client-audit-a-only'},
    data:{model:'audit-test-model',messages:[{role:'system',content:'FORBIDDEN_BROWSER_SYSTEM'},{role:'user',content:input}]}
  });
  assert(result.status()===200, '超长模型提交不应被审计拒绝');
  await openSavedDetail({marker:prefix,truncated:true,incomplete:false,label:'已截断'});
  const visible = await frame.locator('#detail').evaluate((el, expectedPrefix) => {
    const value = el.textContent;
    return {bytes:new TextEncoder().encode(value).length,prefix:value.startsWith(expectedPrefix),onlyExpectedASCII:!(/[^x]/u.test(value.slice(expectedPrefix.length))),tail:value.includes('FORBIDDEN_BROWSER_TAIL'),broken:value.includes('�'),instructions:value.includes('FORBIDDEN_BROWSER_SYSTEM')};
  },prefix);
  assert(visible.bytes===65535 && visible.prefix && visible.onlyExpectedASCII && !visible.tail && !visible.broken && !visible.instructions, '浏览器展示不是已保存的字符安全前缀');
  const screenshot = options.screenshot ?? '.test-data/browser-truncation-label-' + Date.now() + '.png';
  await frame.locator('#detail-meta').screenshot({path:screenshot});
  await frame.locator('#close-detail').click();
  const extraScreenshots = [];
  const hostile = '<img src="https://invalid.example/forbidden-incomplete.png">';
  const partialMarker = 'BROWSER-INCOMPLETE-' + Date.now() + '-';
  const combinedMarker = 'BROWSER-COMBINED-' + Date.now() + '-';
  const combinedPrefix = combinedMarker + 'y'.repeat(65535 - combinedMarker.length);
  const unknown = {type:'future',payload:'FORBIDDEN_BROWSER_UNKNOWN_PAYLOAD'};
  const cases = [
    {marker:partialMarker,content:[{type:'text',text:partialMarker + hostile},{type:'image_url',image_url:{url:'https://invalid.example/FORBIDDEN_BROWSER_IMAGE'}},unknown,{type:'text',text:'后'}],want:partialMarker + hostile + '[图片]后',truncated:false,label:'提取不完整'},
    {marker:combinedMarker,content:[{type:'text',text:combinedPrefix},{type:'text',text:'🙂FORBIDDEN_BROWSER_COMBINED_TAIL'},unknown,{type:'text',text:'z'}],want:combinedPrefix,truncated:true,label:'已截断 · 提取不完整'}
  ];
  let externalLoads = 0;
  /** 统计 request 是否离开合成 origin；无返回，URL 无效时由测试抛错，不保存请求内容。 */
  const onRequest = request => { if (new URL(request.url()).origin !== origin) externalLoads++; };
  page.on('request', onRequest);
  try {
    for (const [i,tc] of cases.entries()) {
      const submitted = await page.request.post(origin + '/v1/chat/completions', {
        headers:{Authorization:'Bearer synthetic-client-audit-a-only'},
        data:{model:'audit-test-model',messages:[{role:'user',content:tc.content}]}
      });
      assert(submitted.status()===200, '不完整内容提交被审计拒绝');
      const detail = await openSavedDetail({...tc,incomplete:true});
      assert(detail.text===tc.want, '管理详情正文不是本轮独立预期值');
      assert((await frame.locator('#detail').textContent())===tc.want, '不完整记录未按纯文本展示已保存部分');
      assert((await frame.locator('#detail img, #detail a, #detail script').count())===0, '输入生成了可执行或外部资源节点');
      const health = await frame.locator('#health').textContent();
      assert(/"unsupported_content":[1-9][0-9]*/.test(health), '提取缺口没有可见诊断');
      const labelScreenshot = screenshot.replace(/\.png$/, i===0?'-incomplete.png':'-combined.png');
      await frame.locator('#detail-meta').screenshot({path:labelScreenshot});
      extraScreenshots.push(require('node:path').basename(labelScreenshot));
      await frame.locator('#close-detail').click();
    }
    assert(externalLoads===0, '不完整正文触发了外部资源加载');
  } finally {
    page.off('request', onRequest);
  }
  // 只对实际进入官方 HTTP 执行回调的 Responses 路由核对正文，失败路由单独验证不采集。
  {
    const model='audit-test-model';
    const marker = 'BROWSER-RESPONSES-' + Date.now() + '-';
    const submitted = await page.request.post(origin + '/v1/responses', {
      headers:{Authorization:'Bearer synthetic-client-audit-a-only'},
      data:{model,input:[{type:'message',role:'user',content:[{type:'input_text',text:marker},{type:'input_image',image_url:'FORBIDDEN_BROWSER_RESPONSES_IMAGE'},unknown]}],previous_response_id:'FORBIDDEN_BROWSER_RESPONSES_PREVIOUS'}
    });
    assert(submitted.status()===200, 'Responses 审计改变了原模型路由结果');
    const detail = await openSavedDetail({marker,truncated:false,incomplete:true,label:'提取不完整'});
    assert(detail.protocol==='responses' && detail.model===model && detail.text===marker+'[图片]', 'Responses 执行入口元数据或正文不正确');
    const row = frame.locator('#records tbody tr').filter({hasText:marker});
    await row.getByRole('cell', {name:'Responses HTTP',exact:true,includeHidden:true}).waitFor();
    await row.getByRole('cell', {name:model,exact:true,includeHidden:true}).waitFor();
    await frame.locator('#detail-meta').filter({hasText:'Responses HTTP · '+model}).waitFor();
    const labelScreenshot = screenshot.replace(/\.png$/, '-responses.png');
    await frame.locator('#detail-meta').screenshot({path:labelScreenshot});
    extraScreenshots.push(require('node:path').basename(labelScreenshot));
    await frame.locator('#close-detail').click();
  }
  const rejectedMarker='BROWSER-UNROUTED-'+require('node:crypto').randomUUID();
  const rejected=await page.request.post(origin+'/v1/responses',{headers:{Authorization:'Bearer synthetic-client-audit-a-only'},data:{model:null,input:rejectedMarker}});
  assert(rejected.status()===400,'缺少模型的 Responses 应由宿主路由拒绝');
  const unobserved=await page.request.post(api+'/records/search',{headers:{Authorization:'Bearer '+admin},data:{q:rejectedMarker}});
  assert(unobserved.status()===200 && (await unobserved.json()).total===0,'未进入 HTTP 执行的请求不应保存正文');
  // Anthropic 的工具结果即使带嵌套文本/附件也不是新用户输入；页面只显示允许正文与固定占位符。
  const anthropicMarker = 'BROWSER-ANTHROPIC-' + Date.now() + '-';
  const anthropicLimitMarker = 'BROWSER-ANTHROPIC-LIMIT-' + Date.now() + '-';
  const anthropicPrefix = anthropicLimitMarker + 'a'.repeat(65535 - anthropicLimitMarker.length);
  const toolResult = {type:'tool_result',tool_use_id:'FORBIDDEN_BROWSER_ANTHROPIC_TOOL_ID',content:[{type:'text',text:'FORBIDDEN_BROWSER_ANTHROPIC_TOOL_TEXT'},{type:'image',source:{type:'url',url:'FORBIDDEN_BROWSER_ANTHROPIC_TOOL_IMAGE'}},{type:'document',source:{type:'file',file_id:'FORBIDDEN_BROWSER_ANTHROPIC_TOOL_FILE'}}]};
  const anthropicCases = [
    {marker:anthropicMarker,content:[toolResult,{type:'text',text:anthropicMarker+hostile},{type:'image',source:{type:'url',url:'FORBIDDEN_BROWSER_ANTHROPIC_IMAGE'}},{type:'document',source:{type:'text',data:'FORBIDDEN_BROWSER_ANTHROPIC_DOCUMENT'}},unknown,{type:'text',text:'后'}],want:anthropicMarker+hostile+'[图片][文件]后',truncated:false,label:'提取不完整'},
    {marker:anthropicLimitMarker,content:[{type:'text',text:anthropicPrefix},{type:'text',text:'🙂FORBIDDEN_BROWSER_ANTHROPIC_TAIL'},unknown],want:anthropicPrefix,truncated:true,label:'已截断 · 提取不完整'}
  ];
  page.on('request', onRequest);
  try {
    for (const [i,tc] of anthropicCases.entries()) {
      const submitted = await page.request.post(origin + '/v1/messages', {
        headers:{Authorization:'Bearer synthetic-client-audit-a-only'},
        data:{model:'audit-test-model',max_tokens:32,system:'FORBIDDEN_BROWSER_ANTHROPIC_SYSTEM',messages:[{role:'user',content:'FORBIDDEN_BROWSER_ANTHROPIC_HISTORY'},{role:'user',content:tc.content}]}
      });
      assert(submitted.status()===200, 'Anthropic 审计改变了原模型调用结果');
      const detail = await openSavedDetail({...tc,incomplete:true});
      assert(detail.protocol==='anthropic-messages' && detail.model==='audit-test-model' && detail.text===tc.want, 'Anthropic 执行入口正文、模型或协议错误');
      const row = frame.locator('#records tbody tr').filter({hasText:tc.marker});
      await row.getByRole('cell', {name:'Anthropic Messages',exact:true,includeHidden:true}).waitFor();
      await row.getByRole('cell', {name:'audit-test-model',exact:true,includeHidden:true}).waitFor();
      await frame.locator('#detail-meta').filter({hasText:'Anthropic Messages · audit-test-model'}).waitFor();
      assert((await frame.locator('#detail').textContent())===tc.want, 'Anthropic 详情未展示已保存纯文本');
      assert((await frame.locator('#detail img, #detail a, #detail script').count())===0, 'Anthropic 输入生成了外部资源节点');
      const labelScreenshot = screenshot.replace(/\.png$/, i===0?'-anthropic.png':'-anthropic-combined.png');
      await frame.locator('#detail-meta').screenshot({path:labelScreenshot});
      extraScreenshots.push(require('node:path').basename(labelScreenshot));
      await frame.locator('#close-detail').click();
    }
    assert(externalLoads===0, 'Anthropic 正文触发了外部资源加载');
  } finally {
    page.off('request', onRequest);
  }
  const notice = await frame.locator('#notice').textContent();
  assert(notice.includes('Responses HTTP') && notice.includes('Anthropic Messages') && notice.includes('tool_result'), '页面未说明实际 HTTP 支持及工具结果边界');
  return {screenshot,extraScreenshots,modelStatus:200,listTruncationLabel:true,detailTruncationLabel:true,incompleteLabels:true,combinedLabels:true,visibleGap:true,literalPlaceholders:true,responsesProtocolLabels:true,responsesExecutionModel:true,unroutedRequestNotCaptured:true,anthropicProtocolLabels:true,anthropicToolBoundary:true,anthropicCombinedLabels:true,externalLoads,visibleBytes:visible.bytes,tailVisible:false,brokenUTF8:false};
}
