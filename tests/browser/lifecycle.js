// 本场景通过真实宿主管理页面验证生命周期操作；只使用合成凭据，证据不包含对象 ID 或输入正文。
// page 指向已就绪的本地合成实例，options 提供有限状态标签截图路径；失败抛出固定说明，成功返回行为证据。
/** 验证 page 的对象改名、移除、恢复和主动暂停；options 指定有限截图，返回证据，行为不符时抛错。 */
module.exports = async (page, options = {}) => {
  /** 验证用户可见条件；condition 为结果，message 为固定说明，失败抛错，成功无返回值。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const origin = new URL(page.url()).origin;
  const admin = 'synthetic-admin-audit-test-only';
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
  await frame.locator('#open-objects').click();
  assert(await frame.getByRole('button', {name:'仅改名',exact:true}).count()===1, '管理页面缺少仅改名操作');
  assert(await frame.getByRole('button', {name:'移除监控',exact:true}).count()===1, '管理页面缺少移除监控操作');
  await frame.locator('#close-management').click();
  assert(await frame.getByRole('button', {name:'暂停采集',exact:true}).count()===1, '管理页面缺少主动暂停操作');
  assert(await frame.getByRole('button', {name:'恢复采集',exact:true}).count()===1, '管理页面缺少恢复采集操作');
  assert(await frame.getByRole('combobox', {name:'历史对象筛选',exact:true}).count()===1, '管理页面缺少历史对象筛选');
  const api = origin + '/v0/management/plugins/request-input-audit';
  const clientKeys = ['synthetic-client-audit-a-only','synthetic-client-audit-b-only'];
  const marker = 'BROWSER-LIFECYCLE-' + Date.now() + '-' + Math.random().toString(16).slice(2) + '-';
  // 本地异步同步通常很快；10秒是有限验收窗口，失败不以旧条数或固定sleep补成通过。
  const savedDeadlineMs = 10000;
  /** 读取受保护公开接口；path 指定路由，返回 JSON，鉴权或读取失败抛出固定错误。 */
  async function read(path) {
    const response = await page.request.get(api+path,{headers:{Authorization:'Bearer '+admin}});
    assert(response.status()===200,'生命周期受保护查询失败');
    return response.json();
  }
  /** 执行页面写操作并等待成功提示；button 为控件、path 为公开路由、message 为完成状态，失败抛错。 */
  async function action(button,path,message) {
    const saved = page.waitForResponse(response=>response.url().endsWith('/request-input-audit'+path)&&response.request().method()==='POST');
    await frame.getByRole('button',{name:button,exact:true}).click();
    assert((await saved).status()===200,'生命周期页面配置未保存');
    await frame.locator('#message').filter({hasText:message}).waitFor();
  }
  /** 提交本轮唯一合成文本；key 选择实际客户端身份，suffix 区分提交，成功无返回，模型异常抛错。 */
  async function submit(key,suffix) {
    const response = await page.request.post(origin+'/v1/chat/completions',{headers:{Authorization:'Bearer '+key},data:{model:'audit-test-model',messages:[{role:'user',content:marker+suffix}]}});
    assert(response.status()===200,'生命周期操作改变了可控模型调用');
  }
  /** 等待本轮唯一输入落盘并核对详情；suffix 指定提交，返回公开记录，超时或身份/内容不一致抛错。 */
  async function saved(suffix) {
    const deadline=Date.now()+savedDeadlineMs;
    while(Date.now()<deadline) {
      const list=await read('/records?limit=100');
      const record=list.records.find(record=>record.preview===marker+suffix);
      if(record) {
        const detail=await read('/detail?id='+encodeURIComponent(record.id));
        assert(detail.id===record.id&&detail.text===marker+suffix&&detail.object_id===record.object_id,'生命周期已保存详情不对应本轮提交');
        return detail;
      }
    }
    throw new Error('生命周期本轮提交未落盘');
  }
  /** 等待公开队列排空；返回基础状态，不用历史条数判断本轮结果，超时抛错。 */
  async function drained() {
    const deadline=Date.now()+savedDeadlineMs;
    while(Date.now()<deadline) {
      const status=await read('/status');
      if(status.state.queued===0)return status.state;
    }
    throw new Error('生命周期公开队列未排空');
  }
  /** 核对本轮输入确实跳过且缺口没有变化；suffix 为提交标记、before 为基准状态，失败抛错，无返回。 */
  async function skipped(suffix,before) {
    const after=await drained();
    const list=await read('/records?limit=100');
    assert(after.saved===before.saved&&!list.records.some(record=>record.preview===marker+suffix),'主动暂停或移除仍产生新副本');
    assert(JSON.stringify(after.gaps)===JSON.stringify(before.gaps),'预期跳过被错误计为缺口');
  }
  const ids=await frame.locator('#objects option').evaluateAll(options=>options.map(option=>option.value));
  assert(ids.length===2,'生命周期合成实例应仅有两个客户端对象');
  await frame.locator('#open-objects').click();
  for(const [index,id]of ids.entries()) {
    await frame.locator('#objects').selectOption(id);
    await frame.getByRole('textbox',{name:'名称',exact:true}).fill('生命周期旧名'+index);
    await action('选择并命名','/monitor','监控配置已保存');
    await frame.locator('#objects option').filter({hasText:'生命周期旧名'+index}).waitFor({state:'attached'});
  }
  await submit(clientKeys[0],'before');
  const before=await saved('before');
  await submit(clientKeys[1],'other');
  const other=await saved('other');
  assert(before.object_id!==other.object_id,'两个客户端 Key 的历史不能合并');
  await frame.locator('#objects').selectOption(before.object_id);
  const renamed='生命周期当前名 <b>纯文本</b>';
  await frame.getByRole('textbox',{name:'名称',exact:true}).fill(renamed);
  await action('仅改名','/rename','名称已保存');
  await submit(clientKeys[0],'after-rename');
  const renamedRecord=await saved('after-rename');
  const oldDetail=await read('/detail?id='+encodeURIComponent(before.id));
  assert(oldDetail.name===renamed&&renamedRecord.name===renamed,'改名后新旧详情未统一显示当前名称');
  await frame.locator('#close-management').click();
  await frame.getByRole('combobox',{name:'历史对象筛选',exact:true}).selectOption(before.object_id);
  await frame.locator('#records tbody tr').filter({hasText:marker+'before'}).waitFor();
  await frame.getByRole('button',{name:'刷新已保存记录',exact:true}).click();
  const oldRow=frame.locator('#records tbody tr').filter({hasText:marker+'before'});
  await oldRow.locator('.object-name').waitFor();
  assert(await oldRow.locator('.object-name').textContent()===renamed,'列表未精确显示当前对象名称');
  await frame.locator('#records tbody tr').filter({hasText:marker+'other'}).waitFor({state:'detached'});
  await oldRow.getByRole('button',{name:'查看',exact:true}).click();
  await frame.locator('#detail').filter({hasText:marker+'before'}).waitFor();
  const rendered=await frame.locator('#detail-meta').evaluate(element=>({text:element.textContent,markup:element.querySelectorAll('b').length}));
  assert(rendered.text.includes(renamed)&&rendered.markup===0,'对象名称必须按当前名称纯文本显示');
  await frame.locator('#close-detail').click();
  await frame.locator('#open-objects').click();
  await action('移除监控','/remove','已移除监控');
  await frame.locator('#objects option').filter({hasText:'已移除但有历史'}).waitFor({state:'attached'});
  await frame.locator('#close-management').click();
  await oldRow.locator('.object-name').waitFor();
  assert(await oldRow.locator('.object-name').textContent()===renamed,'列表未精确显示当前对象名称');
  const removedState=await drained();
  await submit(clientKeys[0],'removed-skip');
  await skipped('removed-skip',removedState);
  const removedHistory=await read('/records?limit=100&object_id='+encodeURIComponent(before.object_id));
  assert(removedHistory.records.some(record=>record.id===before.id)&&removedHistory.records.every(record=>record.object_id===before.object_id),'移除对象的历史入口或筛选失效');
  await frame.locator('#open-objects').click();
  await action('选择并命名','/monitor','监控配置已保存');
  await submit(clientKeys[0],'rejoined');
  const rejoined=await saved('rejoined');
  assert(rejoined.object_id===before.object_id&&rejoined.name===renamed,'同 Key 重新加入没有沿用历史身份');
  await frame.locator('#close-management').click();
  await action('暂停采集','/pause','已主动暂停');
  await frame.locator('#capture-state').filter({hasText:'主动暂停：'}).waitFor();
  const pausedState=await drained();
  assert(pausedState.capture_paused===true&&pausedState.state==='active','主动暂停应与存储故障分离');
  await submit(clientKeys[1],'paused-skip');
  await skipped('paused-skip',pausedState);
  await action('恢复采集','/pause','已恢复主动采集');
  await submit(clientKeys[0],'resumed');
  const resumed=await saved('resumed');
  assert(resumed.object_id===before.object_id,'恢复后的提交归属改变');
  await frame.getByRole('button',{name:'刷新已保存记录',exact:true}).click();
  await frame.locator('#records tbody tr').filter({hasText:marker+'resumed'}).waitFor();
  // 同一宿主下一轮继续使用，结束时保留两对象受监控、未暂停、全部历史筛选的配置。
  await frame.getByRole('combobox',{name:'历史对象筛选',exact:true}).selectOption('');
  await frame.locator('#records tbody tr').filter({hasText:marker+'other'}).waitFor();
  if(options.screenshot)await frame.locator('#capture-state').screenshot({path:options.screenshot});
  return {lifecycleControls:true,renameCurrentHistory:true,removedHistoryFilter:true,rejoinedStableIdentity:true,pauseExpectedSkip:true,resumedCapture:true,modelStatus:200};
};
