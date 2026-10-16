// 本场景通过真实管理页面和公开设置 API 验证保留期确认、原子保存与刷新持久；只改隔离合成实例的配置。
/** 在 page 的本地实例验证配置交互；options 指定状态截图路径，返回有限证据，行为不符或超时抛错。 */
module.exports = async (page, options = {}) => {
  /** 核对 condition；失败以 message 抛错，成功无返回值，不输出凭据或正文。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const origin = new URL(page.url()).origin;
  const admin = 'synthetic-admin-audit-test-only';
  const api = origin + '/v0/management/plugins/request-input-audit';
  const runId = require('node:crypto').randomUUID();
  const posts = [];
  page.on('request', request => {
    if (request.url()===api+'/settings' && request.method()==='POST') posts.push(request.postDataJSON());
  });
  await page.goto(origin + '/management.html');
  await page.waitForFunction(() => document.querySelector('input[type="password"]') || document.querySelector('a[href="#/plugin-pages/request-input-audit/0"]'));
  if (await page.getByRole('textbox', {name:'管理密钥:'}).count()) {
    await page.getByRole('textbox', {name:'管理密钥:'}).fill(admin);
    await page.getByRole('checkbox', {name:'记住密码'}).uncheck();
    await page.getByRole('button', {name:'登录',exact:true}).click();
  }
  await page.getByRole('link', {name:'输入审计',exact:true}).click();
  const frame = page.frameLocator('iframe[title="输入审计"]');
  /** 用合成管理凭据连接页面；成功无返回值，页面或鉴权失败由 Playwright 抛错。 */
  async function login() {
    await frame.getByRole('textbox', {name:'临时管理 Key'}).fill(admin);
    await frame.getByRole('button', {name:'连接',exact:true}).click();
    await frame.getByRole('status').filter({hasText:'管理鉴权通过'}).waitFor();
  }
  /** 读取公开配置并返回 JSON；HTTP 错误抛出固定说明。 */
  async function read() {
    const response = await page.request.get(api+'/settings', {headers:{Authorization:'Bearer '+admin}});
    assert(response.status()===200, '保留期公开配置查询失败');
    return response.json();
  }
  /** 等待页面结束设置操作；成功无返回值，超时由 Playwright 抛错。 */
  async function idle() {
    await page.waitForFunction(() => !document.querySelector('iframe[title="输入审计"]').contentDocument.querySelector('#save-settings').disabled);
  }
  /** 核对页面及公开配置中的 days 和 limit；成功无返回值，不一致抛错。 */
  async function settings(days, limit) {
    await idle();
    assert(await frame.locator('#retention-days').inputValue()===String(days), '页面保留期不符合本轮配置');
    assert(await frame.locator('#max-text-bytes').inputValue()===String(limit), '页面正文上限不符合本轮配置');
    const actual = await read();
    assert(actual.retention_days===days && actual.max_text_bytes===limit, '公开配置没有原子保留本轮两字段');
  }
  /** 填写 days 和 limit 并保存；confirm 决定接受或取消缩短对话框，返回对话框文本，失败抛错。 */
  async function save(days, limit, confirm) {
    if (await frame.locator('#management-dialog').isHidden()) await frame.locator('#open-settings').click();
    await frame.locator('#retention-days').fill(String(days));
    await frame.locator('#max-text-bytes').fill(String(limit));
    const dialog = confirm===undefined ? null : page.waitForEvent('dialog').then(async dialog => {
      const text = dialog.message();
      if (confirm) await dialog.accept(); else await dialog.dismiss();
      assert(text.includes('现有历史') && text.includes('提前到期'), '缩短确认没有明确说明现有历史提前到期');
      return text;
    });
    const response = confirm===false ? null : page.waitForResponse(r => r.url()===api+'/settings' && r.request().method()==='POST');
    await frame.locator('#save-settings').click();
    const text = dialog ? await dialog : '';
    if (response) assert((await response).status()===200, '页面设置保存失败');
    await idle();
    return text;
  }
  await login();
  // 先查控件再改任何配置：旧页面必须在这里失败，而不是误把后端尚未实现当成页面失败。
  assert(await frame.locator('#retention-days').count()===1, '页面缺少保留天数控件');
  await settings(7,65536);
  try {
    const text = await frame.locator('#content').textContent();
    const notice = await frame.locator('#notice').textContent();
    assert(text.includes('1–30') && text.includes('Before 回调所见时刻起算') && text.includes('调长') && text.includes('不延长') && text.includes('每分钟'), '页面未说明执行回调保留期范围、起算或清理语义');
    assert(text.includes('磁盘') && text.includes('空闲页') && text.includes('快照') && text.includes('备份') && text.includes('安全擦除'), '页面未说明逻辑到期和安全擦除的区别');
    assert(notice.includes('应用层不加密') && notice.includes('未收到回调的数量未知') && notice.includes('不保证零丢失') && notice.includes('不适用于生产审计'), '页面丢失加密、执行覆盖或生产/故障恢复限制');
    await save(14,1024);
    await settings(14,1024);
    const beforeCancel = posts.length;
    await save(3,2048,false);
    assert(posts.length===beforeCancel, '取消缩短仍发送了设置 POST');
    const unchanged = await read();
    assert(unchanged.retention_days===14 && unchanged.max_text_bytes===1024, '取消缩短改变了任一配置字段');
    await frame.locator('#close-management').click();
    await frame.locator('#refresh').click();
    await settings(14,1024);
    await frame.locator('#open-settings').click();
    // 任一非法字段都不能提交另一个合法字段，也不能在校验前弹出缩短确认。
    for (const [field,invalid] of [['retention-days',''],['retention-days','0'],['retention-days','31'],['retention-days','1.5'],['max-text-bytes','0'],['max-text-bytes','65537'],['max-text-bytes','1.5']]) {
      await frame.locator('#retention-days').fill('3');
      await frame.locator('#max-text-bytes').fill('2048');
      await frame.locator('#'+field).fill(invalid);
      const before = posts.length;
      let dialogs = 0;
      const dismiss = dialog => { dialogs++; void dialog.dismiss(); };
      page.on('dialog',dismiss);
      try {
        await frame.locator('#save-settings').click();
        await frame.locator('#message').filter({hasText:field==='retention-days'?'保留期必须是 1–30 之间的整数':'正文上限必须是 1–65536 之间的整数'}).waitFor();
        await idle();
      } finally { page.off('dialog',dismiss); }
      assert(posts.length===before && dialogs===0, '非法设置发送了 POST 或提前要求确认');
      const actual = await read();
      assert(actual.retention_days===14 && actual.max_text_bytes===1024, '非法页面值部分改变了配置');
    }
    await save(3,2048,true);
    assert(posts.at(-1).retention_days===3 && posts.at(-1).max_text_bytes===2048 && posts.at(-1).confirm_retention_shortening===true, '缩短确认没有随两字段一起提交');
    await settings(3,2048);
    // 重载 iframe 会丢掉页面内存中的管理 Key，重新鉴权后仍须显示服务端持久设置。
    await frame.locator('body').evaluate(() => location.reload());
    await login();
    await settings(3,2048);
    await frame.locator('#refresh').click();
    await settings(3,2048);
    await save(7,65536);
    await settings(7,65536);
    await frame.locator('#close-management').click();
    if (options.screenshot) await frame.locator('#message').screenshot({path:options.screenshot});
    return {runId,defaultRetentionDays:7,cancelSentNoPost:true,shorteningConfirmed:true,atomicSettings:true,invalidValuesRejected:true,reloadPreservedSettings:true,restoredRetentionDays:7,restoredMaxTextBytes:65536};
  } finally {
    // 只恢复本场景授权的合成配置，不删除历史；失败后也不能让下一轮继承短保留期或正文上限。
    const restored = await page.request.post(api+'/settings', {headers:{Authorization:'Bearer '+admin},data:{retention_days:7,max_text_bytes:65536,confirm_retention_shortening:true}});
    assert(restored.status()===200, '保留期场景未恢复默认配置');
  }
};
