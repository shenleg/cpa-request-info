// 本场景从真实管理页面与公开 API 验证存储故障可见性；文件权限和进程控制只由 runner 的隔离测试控制器执行。
'use strict';
/** 在合成 page 验证首次对象查询失败仍展示状态；options 提供真实故障控制与有限截图，返回证据，失败抛错。 */
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
    await frame.locator('#catalog-state').filter({hasText:'对象加载失败'}).waitFor();
    await frame.locator('#records-state').filter({hasText:'记录加载失败'}).waitFor();
    assert(await frame.locator('#records tbody tr').count()===0,'故障页面把旧列表当作当前结果');
    await options.storageFault(false);
    await options.restart();
    await frame.locator('#refresh').click();
    const row=frame.locator('#records tbody tr').filter({hasText:runId});
    await row.waitFor();
    await row.getByRole('button',{name:'查看',exact:true}).click();
    await frame.locator('#detail').filter({hasText:runId}).waitFor();
    const restarted=await read('/status');
    assert(restarted.state.gaps.unsupported_content===0 && restarted.state.gap_history.unsupported_content,'重启后运行计数或历史缺口不真实');
    assert(restarted.state.recovery.uncertain && restarted.state.recovery.lost_count===null && restarted.state.recovery.lost_from===null && restarted.state.recovery.lost_until===null,'重启不确定损失被冒充已知');
    const history=await frame.locator('#gap-history').textContent(), recovery=await frame.locator('#recovery-state').textContent();
    assert(history.includes('unsupported_content') && history.includes(restarted.state.gap_history.unsupported_content),'页面没有展示历史固定原因和最后时刻');
    assert(recovery.includes('发现时刻：'+restarted.state.recovery.detected_at) && recovery.includes('丢失数量：未知') && recovery.includes('起点 未知，终点 未知') && recovery.includes('不代表损失开始或结束'),'页面把重启未知数量或时段填零/伪造时间');
    await options.storageFault(true);
    await row.getByRole('button',{name:'查看',exact:true}).click();
    await frame.getByRole('status').filter({hasText:'HTTP'}).waitFor();
    assert(!(await frame.locator('#detail').textContent()).includes(runId),'详情读取失败仍展示旧正文');
    await frame.locator('#refresh').click();
    await idle();
    await frame.locator('#records-state').filter({hasText:'记录加载失败'}).waitFor();
    assert(await frame.locator('#records tbody tr').count()===0 && !(await frame.locator('#page').textContent()).includes('匹配'),'刷新失败保留了旧列表或旧匹配数量');
    assert(await frame.locator('#objects option').count()===0 && await frame.locator('#name').inputValue()==='','对象失败保留旧目录或旧名称');
    assert(await frame.locator('#empty').isHidden() && await frame.locator('#next').isDisabled() && await frame.locator('#previous').isDisabled(),'查询失败被误报成空结果或允许旧分页');
    assert((await frame.locator('#capture-state').textContent()).includes('storage_error'),'列表刷新失败遮住了健康状态');
    const failedModel=await page.request.post(origin+'/v1/chat/completions',{headers:{Authorization:'Bearer synthetic-client-audit-a-only'},data:{model:'audit-test-model',messages:[{role:'user',content:runId+'-failed-save'}]}});
    assert(failedModel.status()===200,'真实存储故障拒绝模型调用');
    await options.storageFault(false);
    await options.restart();
    // 初始化失败使 collector 不存在，/settings 真正返回503；不通过浏览器拦截伪造错误或 status。
    await options.storageFault(true,true);
    try {
      await options.restart();
      assert((await page.request.get(api+'/settings',{headers})).status()===503,'启动存储故障未造成真实设置读取失败');
      await frame.locator('#refresh').click();
      await idle();
      await frame.locator('#settings-state').filter({hasText:'设置加载失败'}).waitFor();
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
    if(options.screenshot)await frame.locator('#recovery-state').screenshot({path:options.screenshot});
    return {runId,realStorageFailure:true,firstLoginStatusVisible:true,failedListsCleared:true,failedDetailCleared:true,historySurvivedRestart:true,restartLossUnknown:true,settingsFailureVisible:true,authenticationHidden:true,modelFailOpen:true};
  } finally {
    await options.storageFault(false);
    // 修复权限不自动解除真实存储故障；同目录重启后继续第二轮，不能借用上轮旧结果。
    await options.restart();
  }
};
