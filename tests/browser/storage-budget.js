// 本场景通过真实管理页面及公开 API 验证预算联合保存、容量暂停、超额与调高恢复，不读写私有存储。
/** 在 page 的合成实例验证预算行为；options 指定状态截图路径，返回有限证据，HTTP、页面或期限不符时抛错。 */
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
  /** 用合成凭据连接页面；成功无返回值，鉴权或页面异常由 Playwright 抛错。 */
  async function login() {
    await frame.getByRole('textbox', {name:'临时管理 Key'}).fill(admin);
    await frame.getByRole('button', {name:'连接',exact:true}).click();
    await frame.getByRole('status').filter({hasText:'管理鉴权通过'}).waitFor();
  }
  /** 读取 path 的受保护公开 JSON；返回响应数据，HTTP 失败抛错。 */
  async function read(path) {
    const response = await page.request.get(api+path, {headers:{Authorization:'Bearer '+admin}});
    assert(response.status()===200, '预算场景公开查询失败');
    return response.json();
  }
  /** 以 JSON 提交 filter 查询已保存记录；返回 JSON，HTTP 失败抛错，不将正文关键词写入 URL。 */
  async function search(filter) {
    const response=await page.request.post(api+'/records/search',{headers:{Authorization:'Bearer '+admin},data:filter});
    assert(response.status()===200,'预算场景记录查询失败');
    return response.json();
  }
  /** 等待页面管理操作结束；成功无返回值，超时由 Playwright 抛错。 */
  async function idle() {
    await page.waitForFunction(() => !document.querySelector('iframe[title="输入审计"]').contentDocument.querySelector('#save-settings').disabled);
  }
  /** 填写预算 MiB、保留天数和正文上限；成功无返回值，页面缺失时抛错。 */
  async function fill(mib, days = 7, limit = 65536) {
    if (await frame.locator('#management-dialog').isHidden()) await frame.locator('#open-settings').click();
    await frame.locator('#budget-mib').fill(String(mib));
    await frame.locator('#retention-days').fill(String(days));
    await frame.locator('#max-text-bytes').fill(String(limit));
  }
  /** 保存预算 MiB 及其他两项设置；返回前等待服务端成功和页面刷新，失败抛错。 */
  async function save(mib, days = 7, limit = 65536) {
    await fill(mib,days,limit);
    const response = page.waitForResponse(r => r.url()===api+'/settings' && r.request().method()==='POST');
    await frame.locator('#save-settings').click();
    assert((await response).status()===200, '页面预算保存失败');
    await idle();
  }
  /** 刷新当前公开状态并等待页面结束；成功无返回值，页面或网络失败时抛错。 */
  async function refresh() {
    if (await frame.locator('#management-dialog').isVisible()) await frame.locator('#close-management').click();
    await frame.locator('#refresh').click();
    await idle();
  }
  /** 提交 text 到真实模型入口；无返回值，模型请求受到审计影响时抛错。 */
  async function submit(text) {
    const response = await page.request.post(origin+'/v1/chat/completions', {
      headers:{Authorization:'Bearer synthetic-client-audit-a-only'},
      data:{model:'audit-test-model',messages:[{role:'user',content:text}]}
    });
    assert(response.status()===200, '容量限制改变了模型调用结果');
  }
  await login();
  // 首先只验证页面控件：旧页面应在这里变红，不把后端未实现误算为页面回归。
  assert(await frame.locator('#budget-mib').count()===1, '页面缺少总预算 MiB 控件');
  const initial = await read('/settings');
  assert(initial.budget_bytes===1073741824, '场景未从默认预算开始');
  try {
    const text = await frame.locator('#content').textContent();
    assert(text.includes('4–1024') && text.includes('预留') && text.includes('不是额外额度') && text.includes('可能被拒绝'), '页面未解释预算范围、预留或超额管理限制');
    // 16 MiB 加一字节可精确换算，不能把合法小数 MiB 四舍五入成整数 MiB。
    await save('16.000000953674316',14,1024);
    assert(posts.at(-1).budget_bytes===16777217, 'MiB 没有精确转换为整数字节');
    await fill(8,3,2048);
    const beforeCancel = posts.length;
    const cancelled = page.waitForEvent('dialog').then(dialog => dialog.dismiss());
    await frame.locator('#save-settings').click();
    await cancelled;
    await idle();
    assert(posts.length===beforeCancel, '取消缩短仍提交了预算');
    const unchanged = await read('/settings');
    assert(unchanged.budget_bytes===16777217 && unchanged.retention_days===14 && unchanged.max_text_bytes===1024, '取消确认改变了三项设置');
    for (const value of ['',3,1025,'4.0000001']) {
      await fill(value,3,2048);
      const before = posts.length;
      let dialogs = 0;
      const dismiss = dialog => { dialogs++; void dialog.dismiss(); };
      page.on('dialog',dismiss);
      try {
        await frame.locator('#save-settings').click();
        await frame.locator('#message').filter({hasText:'总预算必须'}).waitFor();
        await idle();
      } finally { page.off('dialog',dismiss); }
      assert(posts.length===before && dialogs===0, '非法预算发送了 POST 或缩短确认');
      const actual = await read('/settings');
      assert(actual.budget_bytes===16777217 && actual.retention_days===14 && actual.max_text_bytes===1024, '非法预算部分改变了设置');
    }
    await fill(16,7,65536);
    const confirmed = page.waitForEvent('dialog').then(async dialog => {
      assert(dialog.message().includes('三项'), '缩短确认未说明预算也一起保存');
      await dialog.accept();
    });
    const saved = page.waitForResponse(r => r.url()===api+'/settings' && r.request().method()==='POST');
    await frame.locator('#save-settings').click();
    await confirmed;
    assert((await saved).status()===200, '三项联合保存失败');
    await idle();
    assert(posts.at(-1).budget_bytes===16777216 && posts.at(-1).retention_days===7 && posts.at(-1).max_text_bytes===65536 && posts.at(-1).confirm_retention_shortening===true, '确认没有随三项设置一起提交');
    await frame.locator('body').evaluate(() => location.reload());
    await login();
    assert(await frame.locator('#budget-mib').inputValue()==='16', '重载后预算没有保留');
    const catalog = await read('/objects');
    for (const object of catalog.objects.filter(object => object.valid)) {
      const monitored = await page.request.post(api+'/monitor', {headers:{Authorization:'Bearer '+admin},data:{object_id:object.id,name:'预算验收对象'}});
      assert(monitored.status()===200, '预算场景未能准备监控对象');
    }
    // 每条正文固定为 64 KiB，70 条本轮记录超过最低预算；不借用前一场景或前一轮历史证明满额。
    for (let index=0;index<70;index++) {
      const marker = runId+'-'+index+'-';
      await submit(marker+'x'.repeat(65536-marker.length));
    }
    const deadline = Date.now()+15000;
    let persisted = false;
    while (Date.now()<deadline) {
      const records = await search({q:runId});
      if (records.total===70) { persisted=true;break; }
    }
    assert(persisted, '本轮预算填充没有全部落盘');
    const filled = await read('/status');
    // 保留 1 MiB 空闲仍不足以覆盖配置/维护预留；验证容量暂停不等同于已经超额。
    await save((filled.state.used_bytes+1048576)/1048576);
    const capacity = await read('/status');
    assert(capacity.available && capacity.state.state==='paused' && capacity.state.reason==='budget_exhausted' && capacity.state.over_budget_bytes===0, '预留不足没有报告容量暂停');
    await frame.locator('#capture-state').filter({hasText:'容量暂停'}).waitFor();
    await save(4);
    const full = await read('/status');
    assert(full.available && full.state.state==='paused' && full.state.reason==='budget_exhausted' && full.state.over_budget_bytes>0, '降低预算没有明确报告超额暂停');
    await frame.locator('#capture-state').filter({hasText:'超预算'}).waitFor();
    const beforeGap = full.state.gaps.budget_exhausted ?? 0;
    await submit(runId+'-CAPACITY-GAP');
    // 模型响应不等待诊断同步；只接受本轮计数增长且异步告警已保存，5秒仅为验收期限。
    const gapDeadline = Date.now()+5000;
    let gap;
    do {
      gap = await read('/status');
      if (gap.state.capacity_gap && gap.state.gaps.budget_exhausted>beforeGap && gap.state.diagnostics_persisted) break;
    } while (Date.now()<gapDeadline);
    assert(gap.state.capacity_gap && gap.state.gaps.budget_exhausted>beforeGap && gap.state.diagnostics_persisted, '容量缺口或持久化告警没有公开状态');
    const retained = await search({q:runId});
    assert(retained.total===70, '降低预算删除了本轮历史或容量缺口被误存为正文');
    await refresh();
    await frame.locator('#health').filter({hasText:'容量告警：已记录'}).waitFor({state:'attached'});
    await frame.locator('#health').filter({hasText:'诊断持久化：已保存'}).waitFor({state:'attached'});
    const health = await frame.locator('#health').textContent();
    assert(health.includes('超额：'+gap.state.over_budget_bytes) && health.includes('配置与维护预留：') && health.includes('恢复所需空闲：'), '状态未展示实际超额、预留和恢复空间');
    await save(16);
    const resumed = await read('/status');
    assert(resumed.state.state==='active' && !resumed.state.capture_paused && resumed.state.capacity_gap, '调高预算没有恢复或清除了先前告警');
    await frame.locator('#capture-state').filter({hasText:'实际可采集'}).waitFor();
    await frame.locator('#close-management').click();
    await frame.locator('#pause').click();
    await idle();
    await frame.locator('#capture-state').filter({hasText:'主动暂停'}).waitFor();
    await save(32);
    const paused = await read('/status');
    assert(paused.state.capture_paused && paused.state.state==='active', '调整预算意外解除了主动暂停');
    await frame.locator('#capture-state').filter({hasText:'主动暂停'}).waitFor();
    await frame.locator('#close-management').click();
    await frame.locator('#resume').click();
    await idle();
    await frame.locator('#capture-state').filter({hasText:'实际可采集'}).waitFor();
    if (options.screenshot) await frame.locator('#capture-state').screenshot({path:options.screenshot});
    return {runId,exactByteConversion:true,atomicThreeSettings:true,cancelSentNoPost:true,invalidBudgetRejected:true,reloadPreservedBudget:true,savedThisRound:70,overBudgetPaused:true,capacityGapPersisted:true,raisedBudgetResumed:true,activePausePreserved:true};
  } finally {
    // 仅恢复合成实例设置；保留历史和容量告警供下一轮检验，不删除持久输出。
    const restored = await page.request.post(api+'/settings', {headers:{Authorization:'Bearer '+admin},data:{budget_bytes:1073741824,retention_days:7,max_text_bytes:65536,confirm_retention_shortening:true}});
    assert(restored.status()===200, '预算场景未恢复默认配置');
    const resumed = await page.request.post(api+'/pause', {headers:{Authorization:'Bearer '+admin},data:{paused:false}});
    assert(resumed.status()===200, '预算场景未恢复主动采集');
  }
};
