// 本文件通过真实宿主管理菜单验证截断、不完整及双状态提示；只用合成凭据，不读私有存储。
// 场景由独立 runner 导入；page 位于合成实例，options 指定本轮截图路径。
// 返回本轮截断/不完整显示与字节边界的有限验收证据；鉴权、落盘或页面校验失败抛错。
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
  for (const [i,id] of ids.entries()) {
    await frame.locator('#objects').selectOption(id);
    await frame.getByRole('textbox', {name:'名称',exact:true}).fill('超长验收对象' + i);
    const saved = page.waitForResponse(r => r.url().endsWith('/request-input-audit/monitor') && r.request().method()==='POST');
    await frame.getByRole('button', {name:'选择并命名'}).click();
    assert((await saved).status()===200, '监控选择未保存');
    await frame.locator('#objects option').filter({hasText:'超长验收对象'+i}).waitFor({state:'attached'});
  }
  /** 等待 fixture.marker 的本轮落盘并打开精确详情；fixture 指定两个标记及可见标签。
   * 返回已核对详情；HTTP、状态或等待失败抛错，不通过旧记录证明本轮成功。 */
  async function openSavedDetail(fixture) {
    let id = '';
    // 给真实 fsync 和管理鉴权查询十秒冷启动余量，不用固定 sleep 假定已经落盘。
    const deadline = Date.now() + 10000;
    while (!id && Date.now()<deadline) {
      const response = await page.request.get(api + '/records', {headers:{Authorization:'Bearer '+admin}});
      assert(response.status()===200, '受保护记录查询失败');
      const list = await response.json();
      const record = list.records.find(r => r.preview.startsWith(fixture.marker) && r.truncated===fixture.truncated && r.extraction_incomplete===fixture.incomplete);
      if (record) id = record.id;
    }
    assert(id !== '', '本轮符合状态的记录没有实际保存');
    await frame.getByRole('button', {name:'刷新已保存记录'}).click();
    const row = frame.locator('#records tbody tr').filter({hasText:fixture.marker});
    await row.getByRole('cell', {name:fixture.label,exact:true}).waitFor();
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
    }
    assert(externalLoads===0, '不完整正文触发了外部资源加载');
  } finally {
    page.off('request', onRequest);
  }
  // Responses 通过同一个真实宿主提交；模型未知不能用上游回复或其他字段补猜。
  for (const [i,model] of ['audit-test-model',null].entries()) {
    const marker = 'BROWSER-RESPONSES-' + i + '-' + Date.now() + '-';
    const submitted = await page.request.post(origin + '/v1/responses', {
      headers:{Authorization:'Bearer synthetic-client-audit-a-only'},
      data:{model,input:[{type:'message',role:'user',content:[{type:'input_text',text:marker},{type:'input_image',image_url:'FORBIDDEN_BROWSER_RESPONSES_IMAGE'},unknown]}],previous_response_id:'FORBIDDEN_BROWSER_RESPONSES_PREVIOUS'}
    });
    assert(submitted.status()===(i===0?200:400), 'Responses 审计改变了原模型路由结果');
    const detail = await openSavedDetail({marker,truncated:false,incomplete:true,label:'提取不完整'});
    assert(detail.protocol==='responses' && detail.model===(model??'') && detail.text===marker+'[图片]', 'Responses 元数据或原协议正文不正确');
    const row = frame.locator('#records tbody tr').filter({hasText:marker});
    await row.getByRole('cell', {name:'Responses HTTP',exact:true}).waitFor();
    await row.getByRole('cell', {name:model??'未知',exact:true}).waitFor();
    await frame.locator('#detail-meta').filter({hasText:'Responses HTTP · '+(model??'未知')}).waitFor();
    const labelScreenshot = screenshot.replace(/\.png$/, i===0?'-responses.png':'-responses-unknown-model.png');
    await frame.locator('#detail-meta').screenshot({path:labelScreenshot});
    extraScreenshots.push(require('node:path').basename(labelScreenshot));
  }
  assert((await frame.locator('#notice').textContent()).includes('Responses HTTP'), '页面未说明实际 HTTP 支持范围');
  return {screenshot,extraScreenshots,modelStatus:200,listTruncationLabel:true,detailTruncationLabel:true,incompleteLabels:true,combinedLabels:true,visibleGap:true,literalPlaceholders:true,responsesProtocolLabels:true,responsesRequestModel:true,unknownRequestModel:true,externalLoads,visibleBytes:visible.bytes,tailVisible:false,brokenUTF8:false};
}
