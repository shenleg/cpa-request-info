// 本文件通过真实宿主管理菜单验证超长输入的列表/详情截断提示；只用合成凭据，不读私有存储。
// 场景由独立 runner 导入；page 位于合成实例，options 指定本轮截图路径。
// 返回本轮截断显示及字节边界的有限验收证据；鉴权、落盘或页面校验失败抛错。
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
  // 65535 个 ASCII 后放一个四字节字符，64KiB 前缀必须停在字符之前，不能切断 UTF-8。
  const prefix = 'BROWSER-LIMIT-' + Date.now() + '-';
  const input = prefix + 'x'.repeat(65535 - prefix.length) + '🙂FORBIDDEN_BROWSER_TAIL';
  const result = await page.request.post(origin + '/v1/chat/completions', {
    headers:{Authorization:'Bearer synthetic-client-audit-a-only'},
    data:{model:'audit-test-model',messages:[{role:'system',content:'FORBIDDEN_BROWSER_SYSTEM'},{role:'user',content:input}]}
  });
  assert(result.status()===200, '超长模型提交不应被审计拒绝');
  let id = '';
  const deadline = Date.now() + 10000;
  while (!id && Date.now()<deadline) {
    const response = await page.request.get(api + '/records', {headers:{Authorization:'Bearer '+admin}});
    assert(response.status()===200, '受保护记录查询失败');
    const list = await response.json();
    const record = list.records.find(r => r.preview.startsWith(prefix) && r.truncated);
    if (record) id = record.id;
  }
  assert(id !== '', '期限内没有实际落盘的截断记录');
  await frame.getByRole('button', {name:'刷新已保存记录'}).click();
  const row = frame.locator('#records tbody tr').filter({hasText:prefix});
  await row.getByRole('cell', {name:'已截断',exact:true}).waitFor();
  const detailResponse = page.waitForResponse(r => r.url().includes('/request-input-audit/detail?') && new URL(r.url()).searchParams.get('id')===id);
  await row.getByRole('button', {name:'查看',exact:true}).click();
  const savedDetail = await detailResponse;
  assert(savedDetail.status()===200 && (await savedDetail.json()).id===id, '详情不是本次实际落盘的记录');
  await frame.locator('#detail-meta').filter({hasText:'已截断'}).waitFor();
  await frame.locator('#detail').filter({hasText:prefix}).waitFor();
  const visible = await frame.locator('#detail').evaluate((el, expectedPrefix) => {
    const value = el.textContent;
    return {bytes:new TextEncoder().encode(value).length,prefix:value.startsWith(expectedPrefix),onlyExpectedASCII:!(/[^x]/u.test(value.slice(expectedPrefix.length))),tail:value.includes('FORBIDDEN_BROWSER_TAIL'),broken:value.includes('�'),instructions:value.includes('FORBIDDEN_BROWSER_SYSTEM')};
  },prefix);
  assert(visible.bytes===65535 && visible.prefix && visible.onlyExpectedASCII && !visible.tail && !visible.broken && !visible.instructions, '浏览器展示不是已保存的字符安全前缀');
  const screenshot = options.screenshot ?? '.test-data/browser-truncation-label-' + Date.now() + '.png';
  await frame.locator('#detail-meta').screenshot({path:screenshot});
  return {screenshot,modelStatus:200,listTruncationLabel:true,detailTruncationLabel:true,visibleBytes:visible.bytes,tailVisible:false,brokenUTF8:false};
}
