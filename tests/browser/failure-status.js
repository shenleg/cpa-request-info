// 本场景从真实管理页面与公开 API 验证存储故障可见性；文件权限和进程控制只由 runner 的隔离测试控制器执行。
'use strict';
/** 在合成 page 验证故障可见性、缺口已阅和自动提醒；options 提供真实故障控制与有限截图，返回证据，失败抛错。 */
module.exports = async (page, options = {}) => {
  /** 核对公开行为 condition；不符合时抛出固定 message，成功无返回值。 */
  const assert = (condition, message) => {
    if (condition) return;
    const error = new Error(message);Error.captureStackTrace(error,assert);throw error;
  };
  const origin = new URL(page.url()).origin;
  const api = origin+'/v0/management/plugins/request-input-audit';
  const admin = 'synthetic-admin-audit-test-only';
  const headers = {Authorization:'Bearer '+admin};
  const runId = require('node:crypto').randomUUID();
  /** 查询受保护公开 path，返回 JSON；HTTP 失败抛错，不打印响应正文。 */
  async function read(path) {
    const response = await page.request.get(api+path,{headers});
    assert(response.status()===200,'故障场景公开查询失败');
    return response.json();
  }
  const catalog = await read('/objects');
  for (const object of catalog.objects.filter(object=>object.valid)) {
    const response = await page.request.post(api+'/monitor',{headers,data:{object_id:object.id,name:'故障验收合成对象'}});
    assert(response.status()===200,'故障场景准备监控失败');
  }
  /** 提交本轮 content 至真实模型入口；返回前确认原模型响应，失败抛错。 */
  async function submit(content) {
    const response=await page.request.post(origin+'/v1/chat/completions',{headers:{Authorization:'Bearer synthetic-client-audit-a-only'},data:{model:'audit-test-model',messages:[{role:'user',content}]}});
    assert(response.status()===200,'故障场景审计改变模型调用');
  }
  /** 等待当前页面操作结束；无返回值，超时由 Playwright 抛错。 */
  async function idle() {await page.waitForFunction(()=>!document.querySelector('iframe[title="输入审计"]').contentDocument.querySelector('#refresh').disabled);}
  // 真实未知内容产生可持久化基础缺口；不从已有历史或当前计数反推重启后的历史结果。
  await submit([{type:'text',text:runId},{type:'future-browser-audit',payload:'synthetic unknown only'}]);
  const deadline=Date.now()+10000;
  let saved=false;
  while(Date.now()<deadline) {
    if((await read('/records?q='+runId)).total===1){saved=true;break;}
  }
  assert(saved,'本轮故障种子未落盘');
  await page.goto(origin+'/management.html');
  await page.waitForFunction(()=>document.querySelector('input[type="password"]')||document.querySelector('a[href="#/plugin-pages/request-input-audit/0"]'));
  if(await page.getByRole('textbox',{name:'管理密钥:'}).count()) {
    await page.getByRole('textbox',{name:'管理密钥:'}).fill(admin);
    await page.getByRole('checkbox',{name:'记住密码'}).uncheck();
    await page.getByRole('button',{name:'登录',exact:true}).click();
  }
  await page.getByRole('link',{name:'输入审计',exact:true}).click();
  const frame=page.frameLocator('iframe[title="输入审计"]');
  await options.storageFault(true);
  try {
    // 首次 /objects 真实读取失败，旧页面会停在登录区，健康状态从未显示。
    assert((await page.request.get(api+'/objects',{headers})).status()===503,'对象查询没有发生真实存储故障');
    assert((await page.request.get(api+'/records',{headers})).status()!==200,'列表查询没有发生真实存储故障');
    await frame.getByRole('textbox',{name:'临时管理 Key'}).fill(admin);
    await frame.getByRole('button',{name:'连接',exact:true}).click();
    await frame.locator('#content').waitFor({state:'visible'});
    await frame.locator('#capture-state').filter({hasText:'storage_error'}).waitFor();
    await frame.locator('#catalog-state').filter({hasText:'对象加载失败'}).waitFor({state:'attached'});
    await frame.locator('#records-state').filter({hasText:'记录加载失败'}).waitFor();
    assert(await frame.locator('#records tbody tr').count()===0,'故障页面把旧列表当作当前结果');
    await options.storageFault(false);
    await options.restart();
    await frame.locator('#refresh').click();
    const row=frame.locator('#records tbody tr').filter({hasText:runId});
    await row.waitFor();
    await row.getByRole('button',{name:'查看',exact:true}).click();
    await frame.locator('#detail').filter({hasText:runId}).waitFor();
    await frame.locator('#close-detail').click();
    const restarted=await read('/status');
    assert(restarted.state.gaps.unsupported_content===0 && restarted.state.gap_history.unsupported_content,'重启后运行计数或历史缺口不真实');
    assert(restarted.state.recovery.uncertain && restarted.state.recovery.lost_count===null && restarted.state.recovery.lost_from===null && restarted.state.recovery.lost_until===null,'重启不确定损失被冒充已知');
    const history=await frame.locator('#gap-history').textContent(), recovery=await frame.locator('#recovery-state').textContent();
    assert(history.includes('unsupported_content') && history.includes(restarted.state.gap_history.unsupported_content),'页面没有展示历史固定原因和最后时刻');
    assert(recovery.includes('发现时刻：'+restarted.state.recovery.detected_at) && recovery.includes('丢失数量：未知') && recovery.includes('起点 未知，终点 未知') && recovery.includes('不代表损失开始或结束'),'页面把重启未知数量或时段填零/伪造时间');
    // 未知块的允许正文已保存；历史必须明确是部分提取，不能冒充整条写入失败。
    assert(history.includes('提取不完整的提交') && history.includes('不等于整条写入失败'),'历史未区分部分提取与整条写入失败');
    assert((await frame.locator('#gap-unread').textContent()).includes('有未阅缺口提醒'),'真实新缺口没有未阅提醒');
    // 确认只修改提醒，不删除正文、缺口历史或重启不确定性。
    await frame.locator('#open-diagnostics').click();
    await frame.getByRole('button',{name:'确认当前提醒已阅',exact:true}).click();await idle();
    await frame.locator('#gap-unread').filter({hasText:'当前缺口提醒均已阅'}).waitFor();
    assert((await frame.locator('#gap-history').textContent()).includes('unsupported_content') && (await read('/records?q='+runId)).total===1,'已阅确认删除历史或输入');
    assert((await frame.locator('#recovery-state').textContent()).includes('丢失数量：未知'),'已阅确认改变重启完整性结论');
    await frame.locator('#close-management').click();
    await options.restart();
    await frame.locator('#refresh').click();await idle();
    assert((await read('/status')).state.gap_alerts.items.find(item=>item.reason==='unsupported_content').unread===false,'已阅确认没有跨重启保存');
    assert(/unsupported_content[^\n]*已阅/.test(await frame.locator('#gap-history').textContent()),'页面重启后未保留已阅历史');
    // 新的真实未知块应由定时状态查询重新提醒；草稿与记录查询不能被后台刷新改动。
    await frame.locator('#open-settings').click();
    await frame.locator('#max-text-bytes').fill('321');
    await frame.locator('#close-management').click();
    await frame.locator('#keyword-filter').fill('未提交的合成筛选');
    const backgroundPaths=[];
    /** 记录本轮 page 请求的保护路径，供自动刷新范围断言；无返回值，不修改请求。 */
    const observe=request=>{const url=new URL(request.url());if(url.pathname.startsWith('/v0/management/plugins/request-input-audit/'))backgroundPaths.push(url.pathname.slice(new URL(api).pathname.length));};
    page.on('request',observe);
    try {
      await submit([{type:'text',text:runId+'-new-gap'},{type:'future-browser-audit',payload:'synthetic unknown only'}]);
      await frame.locator('#gap-history').filter({hasText:/unsupported_content[^\n]*未阅/}).waitFor({state:'attached',timeout:20000});
      assert(backgroundPaths.length>0 && backgroundPaths.every(path=>path==='/status'),'自动刷新读取了状态以外的保护接口');
      assert(await frame.locator('#max-text-bytes').inputValue()==='321' && await frame.locator('#keyword-filter').inputValue()==='未提交的合成筛选','自动刷新覆盖设置或筛选草稿');
      assert((await frame.locator('#gap-unread').textContent()).includes('有未阅缺口提醒'),'新缺口没有恢复未阅提醒');
    } finally {page.off('request',observe);}
    await options.storageFault(true);
    await row.getByRole('button',{name:'查看',exact:true}).click();
    await frame.locator('#message').filter({hasText:'HTTP'}).waitFor();
    assert(!(await frame.locator('#detail').textContent()).includes(runId),'详情读取失败仍展示旧正文');
    await frame.locator('#close-detail').click();
    await frame.locator('#refresh').click();
    await idle();
    await frame.locator('#records-state').filter({hasText:'记录加载失败'}).waitFor();
    assert(await frame.locator('#records tbody tr').count()===0 && !(await frame.locator('#page').textContent()).includes('匹配'),'刷新失败保留了旧列表或旧匹配数量');
    assert(await frame.locator('#metric-matched').textContent()==='未知','列表失败把摘要匹配数当作已知零值');
    assert(await frame.locator('#objects option').count()===0 && await frame.locator('#name').inputValue()==='','对象失败保留旧目录或旧名称');
    assert(await frame.locator('#empty').isHidden() && await frame.locator('#next').isDisabled() && await frame.locator('#previous').isDisabled(),'查询失败被误报成空结果或允许旧分页');
    assert((await frame.locator('#capture-state').textContent()).includes('storage_error'),'列表刷新失败遮住了健康状态');
    const failedModel=await page.request.post(origin+'/v1/chat/completions',{headers:{Authorization:'Bearer synthetic-client-audit-a-only'},data:{model:'audit-test-model',messages:[{role:'user',content:runId+'-failed-save'}]}});
    assert(failedModel.status()===200,'真实存储故障拒绝模型调用');
    await frame.locator('#refresh').click();await idle();
    await frame.locator('#gap-active').filter({hasText:'storage_error'}).waitFor({state:'attached'});
    await frame.locator('#open-diagnostics').click();
    await frame.getByRole('button',{name:'确认当前提醒已阅',exact:true}).click();await idle();
    assert((await frame.locator('#gap-unread').textContent()).includes('当前缺口提醒均已阅'),'活动故障的已阅确认未生效');
    assert(/storage_error[^\n]*故障仍活动[^\n]*已阅/.test(await frame.locator('#gap-active').textContent()),'已阅确认隐藏或解除了活动故障');
    assert(!(await frame.locator('#capture-state').textContent()).includes('实际可采集：'),'已阅确认把活动存储故障报告为正常');
    await frame.locator('#close-management').click();
    await options.storageFault(false);
    await options.restart();
    // 初始化失败使 collector 不存在，/settings 真正返回503；不通过浏览器拦截伪造错误或 status。
    await options.storageFault(true,true);
    try {
      await options.restart();
      assert((await page.request.get(api+'/settings',{headers})).status()===503,'启动存储故障未造成真实设置读取失败');
      await frame.locator('#refresh').click();
      await idle();
      await frame.locator('#settings-state').filter({hasText:'设置加载失败'}).waitFor({state:'attached'});
      assert(await frame.locator('#content').isVisible(),'设置失败隐藏了已认证健康状态');
      for(const id of ['budget-mib','retention-days','max-text-bytes'])assert(await frame.locator('#'+id).inputValue()==='' && await frame.locator('#'+id).isDisabled(),'设置失败保留或启用了旧设置');
      assert(await frame.locator('#save-settings').isDisabled(),'未知设置仍可保存');
      assert((await frame.locator('#capture-state').textContent()).includes('不可用'),'启动故障未展示实际不可用');
    } finally {
      await options.storageFault(false,true);
      await options.restart();
    }
    await frame.locator('#refresh').click();await idle();
    // 真实错误 Key 必须恢复登录并隐藏所有受保护内容，不能因状态优先展示绕过鉴权。
    await frame.locator('body').evaluate(()=>location.reload());
    await frame.getByRole('textbox',{name:'临时管理 Key'}).fill('synthetic-wrong-management-only');
    await frame.getByRole('button',{name:'连接',exact:true}).click();
    await frame.getByRole('status').filter({hasText:'管理鉴权失败'}).waitFor();
    assert(await frame.locator('#content').isHidden() && await frame.locator('#login').isVisible(),'鉴权失败仍显示受保护内容');
    await frame.getByRole('textbox',{name:'临时管理 Key'}).fill(admin);
    await frame.getByRole('button',{name:'连接',exact:true}).click();
    await frame.getByRole('status').filter({hasText:'管理鉴权通过'}).waitFor();
    assert((await frame.locator('#notice').textContent()).includes('fail-open') && (await frame.locator('#notice').textContent()).includes('进程级崩溃不隔离'),'页面缺少常规fail-open与native不隔离说明');
    // 状态接口失败必须清除上一次运行摘要；真实存储故障不一定令 /status HTTP 失败，因此单独模拟传输边界。
    const statusRoute=api+'/status';
    /** 使状态接口返回 503；route 为本轮状态请求，无返回值，不伪造健康 JSON。 */
    const unavailable=route=>route.fulfill({status:503,contentType:'application/json',body:'{}'});
    await page.route(statusRoute,unavailable);
    try {
      await frame.locator('#refresh').click();await idle();
      await frame.locator('#capture-state').filter({hasText:'采集状态加载失败'}).waitFor();
      for(const id of ['metric-saved','metric-queued','metric-used'])assert(await frame.locator('#'+id).textContent()==='未知','状态失败保留旧摘要或把未知当作零');
    } finally {await page.unroute(statusRoute,unavailable);}
    await frame.locator('#refresh').click();await idle();
    // 已连接后撤权与首次错误 Key 不同：原生模态即使仍在 top layer 也必须关闭，不能继续展示受保护正文。
    for(const [code,dialog] of [[401,'detail'],[403,'management']]) {
      if(dialog==='detail') {
        const current=frame.locator('#records tbody tr').filter({hasText:runId}).first();
        await current.getByRole('button',{name:'查看',exact:true}).click();
        await frame.locator('#detail').filter({hasText:runId}).waitFor();
      } else await frame.locator('#open-settings').click();
      await frame.locator('#'+dialog+'-dialog').waitFor({state:'visible'});
      /** 让已连接页面的下一次状态轮询返回 code；route 为请求，无返回值，不改变模型/管理服务端状态。 */
      const revoked=route=>route.fulfill({status:code,contentType:'application/json',body:'{}'});
      await page.route(statusRoute,revoked);
      try {
        // 使用真正的十秒状态轮询触发撤权，不对被原生模态阻挡的主页面按钮做程序化点击。
        await frame.locator('#content').waitFor({state:'hidden',timeout:20000});
        assert(await frame.locator('#detail-dialog').isHidden() && await frame.locator('#management-dialog').isHidden() && await frame.locator('#login').isVisible(),'撤权后模态仍显示受保护内容');
      } finally {await page.unroute(statusRoute,revoked);}
      await frame.getByRole('textbox',{name:'临时管理 Key'}).fill(admin);
      await frame.getByRole('button',{name:'连接',exact:true}).click();
      await frame.getByRole('status').filter({hasText:'管理鉴权通过'}).waitFor();
    }
    await frame.locator('#open-diagnostics').click();
    if(options.screenshot)await frame.locator('#recovery-state').screenshot({path:options.screenshot});
    await frame.locator('#close-management').click();
    return {runId,realStorageFailure:true,firstLoginStatusVisible:true,failedListsCleared:true,failedDetailCleared:true,historySurvivedRestart:true,restartLossUnknown:true,partialExtractionDistinct:true,acknowledgementSurvivedRestart:true,newGapUnread:true,activeFaultVisibleAfterAcknowledgement:true,statusOnlyAutoRefresh:true,draftsPreserved:true,settingsFailureVisible:true,failedSummaryUnknown:true,revokedDialogsClosed:true,authenticationHidden:true,modelFailOpen:true};
  } finally {
    await options.storageFault(false);
    // 修复权限不自动解除真实存储故障；同目录重启后继续第二轮，不能借用上轮旧结果。
    await options.restart();
  }
};
