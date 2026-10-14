// 本文件通过真实宿主管理页、插件菜单及同源受保护接口验收；仅使用合成 Key，不保存认证材料。
// 用 Playwright 的 browser_run_code 工具读取此文件；先导航到 audit-demo 输出的 management.html。
async (page) => {
  /** 验证 condition；失败时以 message 抛出错误，成功无返回值，不输出凭据或正文。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const admin = 'synthetic-admin-audit-test-only';
  const base = new URL(page.url()).origin;
  const clientKeys = ['synthetic-client-audit-a-only', 'synthetic-client-audit-b-only'];
  const text = '<script>window.__auditInjected=true</script>\n<img src="https://example.invalid/audit-forbidden-image">\n![图片](https://example.invalid/audit-forbidden-markdown)';
  const external = [];
  page.on('request', request => { if (request.url().includes('audit-forbidden')) external.push(request.url()); });
  await page.goto(base + '/management.html');
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
  const ids = await frame.locator('#objects option').evaluateAll(options => options.map(option => option.value));
  assert(ids.length === 2, '应有两个无秘密合成对象');
  for (const [i,id] of ids.entries()) {
    await frame.locator('#objects').selectOption(id);
    await frame.getByRole('textbox',{name:'名称',exact:true}).fill('浏览器合成对象' + i);
    const response = page.waitForResponse(r => r.url().endsWith('/request-input-audit/monitor') && r.request().method() === 'POST');
    await frame.getByRole('button',{name:'选择并命名'}).click();
    assert((await response).status() === 200, '监控配置未保存');
    await frame.locator('#objects option').filter({hasText:'浏览器合成对象'+i}).waitFor({state:'attached'});
  }
  for (const key of clientKeys) {
    const result = await page.request.post(base+'/v1/chat/completions',{headers:{Authorization:'Bearer '+key},data:{model:'audit-test-model',messages:[{role:'system',content:'FORBIDDEN_BROWSER_SYSTEM'},{role:'user',content:'FORBIDDEN_BROWSER_HISTORY'},{role:'user',content:text}]}});
    assert(result.status() === 200,'实际可控模型应成功');
  }
  // 等待本次已保存数据真正替换旧行，不能把旧页面文本误当作本次请求结果。
  await frame.getByRole('button',{name:'刷新已保存记录'}).click();
  await frame.locator('#records tbody tr').first().filter({hasText:'![图片]'}).waitFor();
  await frame.getByRole('button',{name:'查看',exact:true}).first().click();
  await frame.locator('#detail').filter({hasText:'![图片]'}).waitFor();
  const result = await frame.locator('body').evaluate(body => {
    const w = body.ownerDocument.defaultView;
    return {injected:w.__auditInjected === true,images:body.querySelectorAll('#detail img,#records img').length,text:body.querySelector('#detail').textContent,keyFieldEmpty:body.querySelector('#management-key').value==='',pluginStorage:Object.keys(w.localStorage).filter(k=>k.includes('audit'))};
  });
  assert(result.text === text && !result.injected && result.images === 0,'正文必须按字面纯文本显示');
  assert(result.keyFieldEmpty && result.pluginStorage.length === 0,'不得新增持久化管理 Key');
  assert(external.length === 0,'不能自动加载输入中的外部资源');
  return {menu:true,selection:true,details:true,plainText:true,externalRequests:0,keyStoredByPlugin:false};
}
