// 本模块复用 search-browse 的本轮夹具，在真实宿主 iframe 内验证原型 A 布局、主题和模态交互，不新增场景或保存正文截图。
'use strict';

/** 验证 page 的桌面/窄屏布局；fixture 提供本轮 keyword、recordId 和 text，返回有限证据，几何或交互不符时抛错。 */
module.exports = async (page, fixture) => {
  /** 校验 condition；失败抛固定 message，成功无返回值，不输出正文或认证材料。 */
  const assert = (condition, message) => { if (!condition) throw new Error(message); };
  const frame = page.frameLocator('iframe[title="输入审计"]');
  const api = new URL(page.url()).origin+'/v0/management/plugins/request-input-audit';
  const viewport = page.viewportSize();
  const initialTheme = await frame.locator('html').getAttribute('data-theme');
  const paths = [];
  /** 记录页面保护接口的路径；request 为页面请求，无返回值，不保存查询词和认证头。 */
  const observe = request => { const url=new URL(request.url());if(url.pathname.startsWith(new URL(api).pathname+'/'))paths.push(url.pathname.slice(new URL(api).pathname.length)); };
  /** 等待列表及摘要使用 response 的匹配数；返回公开查询结果，HTTP 或页面不符时抛错。 */
  async function matched(response) {
    assert(response.status()===200,'布局验收查询失败');
    const result=await response.json();
    await page.waitForFunction(total=>{
      const doc=document.querySelector('iframe[title="输入审计"]').contentDocument;
      return !doc.querySelector('#apply-filters').disabled && doc.querySelector('#metric-matched').textContent===String(total);
    },result.total);
    return result;
  }
  /** 点击 selector 发起查询；range 可指定预设端点契约，返回本次结果，查询或端点不符时抛错。 */
  async function query(selector, range) {
    const pending=page.waitForResponse(response=>response.url()===api+'/records/search'&&response.request().method()==='POST');
    await frame.locator(selector).click();
    const response=await pending;
    if(range) {
      const filter=response.request().postDataJSON();
      assert(Boolean(filter.from)===(range!=='all') && !filter.to,'时间预设未使用预期的开放终点 JSON 查询');
      assert(new URL(response.url()).search==='', '时间筛选不得进入 URL');
    }
    return matched(response);
  }
  /** 返回 iframe 文档宽度及其可视宽度；无外部输入，页面不存在时抛错，不修改滚动容器。 */
  async function widths() {
    return frame.locator('html').evaluate(root=>({scroll:root.scrollWidth,client:root.clientWidth,body:root.ownerDocument.body.scrollWidth}));
  }
  page.on('request',observe);
  try {
    await page.setViewportSize({width:1200,height:900});
    await page.evaluate(()=>{window.scrollTo(0,0);document.querySelector('iframe[title="输入审计"]').contentWindow.scrollTo(0,0);});
    const iframeBox=await page.locator('iframe[title="输入审计"]').boundingBox();
    const header=await frame.locator('#records thead').boundingBox();
    assert(iframeBox && header && header.y>=iframeBox.y && header.y+header.height<=Math.min(900,iframeBox.y+iframeBox.height),'1200×900 宿主首屏没有完整显示记录表头');
    assert(await frame.locator('#capture-state').isVisible(),'重要采集状态不在主页面');

    const storageBefore=await frame.locator('body').evaluate(body=>JSON.stringify(Object.entries(body.ownerDocument.defaultView.localStorage).sort()));
    const themes=[];
    for(const theme of ['light','dark']) {
      if(await frame.locator('html').getAttribute('data-theme')!==theme)await frame.locator('#theme-toggle').click();
      assert(await frame.locator('html').getAttribute('data-theme')===theme,'主题按钮未切换 html data-theme');
      themes.push(await frame.locator('body').evaluate(body=>({color:getComputedStyle(body).color,background:getComputedStyle(body).backgroundColor})));
    }
    assert(JSON.stringify(themes[0])!==JSON.stringify(themes[1]),'浅深主题没有改变实际页面颜色');
    assert(await frame.locator('body').evaluate(body=>JSON.stringify(Object.entries(body.ownerDocument.defaultView.localStorage).sort()))===storageBefore,'主题切换新增或修改了持久化状态');

    await frame.locator('#keyword-filter').fill(fixture.keyword);
    await query('#apply-filters');
    const originalLimit=await frame.locator('#max-text-bytes').inputValue();
    const draft=originalLimit==='123'?'124':'123';
    const beforeOpen=paths.length;
    await frame.locator('#open-settings').click();
    await frame.locator('#max-text-bytes').fill(draft);
    await frame.locator('#close-management').click();
    await frame.locator('#open-objects').click();
    assert(await frame.locator('#objects').isVisible(),'对象入口未显示对象面板');
    await frame.locator('#close-management').click();
    await frame.locator('#open-diagnostics').click();
    assert(await frame.locator('#health').isVisible(),'诊断入口未显示状态面板');
    await frame.locator('#close-management').click();
    await frame.locator('#open-settings').click();
    assert(await frame.locator('#max-text-bytes').inputValue()===draft && await frame.locator('#keyword-filter').inputValue()===fixture.keyword,'打开管理面板改写了设置或筛选草稿');
    assert(paths.slice(beforeOpen).every(path=>path==='/status'),'打开管理面板重新加载了对象、设置或记录');
    await frame.locator('#max-text-bytes').fill(originalLimit);
    await frame.locator('#close-management').click();

    for(const range of ['today','week']) {
      await query('button[data-range="'+range+'"]',range);
      assert(await frame.locator('#from-filter').inputValue()!=='' && await frame.locator('#to-filter').inputValue()==='','时间预设未填写起点或错误限制终点');
    }
    const beforeCustom=paths.length;
    await frame.locator('button[data-range="custom"]').click();
    assert(await frame.locator('#from-filter').evaluate(input=>input.ownerDocument.activeElement===input),'自定义时间没有聚焦起点输入');
    assert(paths.slice(beforeCustom).every(path=>path==='/status'),'自定义时间提前执行了查询');
    await query('button[data-range="all"]','all');
    assert(await frame.locator('#from-filter').inputValue()==='' && await frame.locator('#to-filter').inputValue()==='','全部时间没有清空端点');

    await page.setViewportSize({width:390,height:900});
    const narrow=await widths();
    // 表格本身允许局部横向滚动；禁止让 iframe 整个文档随列宽向右溢出。
    assert(narrow.client<=390 && narrow.scroll<=narrow.client && narrow.body<=narrow.client,'390px 下 iframe 未随宿主缩小或文档出现横向溢出');
    const scrollable=await frame.locator('#records').evaluate(table=>{
      for(let parent=table.parentElement;parent!==table.ownerDocument.body;parent=parent.parentElement) {
        if(['auto','scroll'].includes(getComputedStyle(parent).overflowX))return parent.scrollWidth>parent.clientWidth && parent.getBoundingClientRect().width<=table.ownerDocument.documentElement.clientWidth;
      }
      return false;
    });
    assert(scrollable,'窄屏表格没有独立横向滚动容器');
    const row=frame.locator('#records tbody tr').filter({hasText:fixture.keyword+'ENTRY-22'});
    const opener=row.getByRole('button',{name:'查看',exact:true});
    const pending=page.waitForResponse(response=>response.url().startsWith(api+'/detail?') && new URL(response.url()).searchParams.get('id')===fixture.recordId);
    await opener.click();
    assert((await pending).status()===200,'布局验收未打开本轮详情');
    await frame.locator('#detail-dialog').waitFor({state:'visible'});
    await frame.locator('#detail').filter({hasText:fixture.keyword}).waitFor();
    assert(await frame.locator('#detail').textContent()===fixture.text && await frame.locator('#detail img,#detail script,#detail style,#detail a').count()===0,'模态详情没有保持恶意内容的纯文本');
    const dialogWidth=await widths();
    assert(dialogWidth.scroll<=dialogWidth.client && dialogWidth.body<=dialogWidth.client,'390px 详情导致 iframe 文档横向溢出');
    await page.context().grantPermissions(['clipboard-read','clipboard-write']);
    await frame.locator('#copy-detail').click();
    await frame.locator('#copy-message').filter({hasText:'已复制已保存纯文本'}).waitFor();
    assert(await frame.locator('body').evaluate(body=>body.ownerDocument.defaultView.navigator.clipboard.readText())===fixture.text,'Clipboard API 未复制实际详情原文');
    await page.keyboard.press('Escape');
    await frame.locator('#detail-dialog').waitFor({state:'hidden'});
    assert(await opener.evaluate(button=>button.ownerDocument.activeElement===button),'Escape 关闭详情没有返回原查看按钮焦点');
    // P2 回归：详情仍在请求中时查看按钮被 busy 禁用，关闭事件不能因此永久丢失焦点恢复。
    let releaseDetail, markHeld;
    const released=new Promise(resolve=>{releaseDetail=resolve;});
    const held=new Promise(resolve=>{markHeld=resolve;});
    const detailRoute=api+'/detail?id='+encodeURIComponent(fixture.recordId);
    /** 阻塞本轮详情请求直到测试释放；route 为真实请求，无返回值，继续请求失败时抛错。 */
    const delayedDetail=async route=>{markHeld();await released;await route.continue();};
    await page.route(detailRoute,delayedDetail);
    try {
      await opener.click();
      await held;
      await frame.locator('#detail-dialog').waitFor({state:'visible'});
      assert(await frame.locator('#copy-detail').isDisabled(),'加载中仍允许复制旧正文');
      await page.keyboard.press('Escape');
      await frame.locator('#detail-dialog').waitFor({state:'hidden'});
      assert(await opener.isDisabled(),'详情请求未阻塞，未覆盖关闭时查看按钮仍禁用的回归条件');
      releaseDetail();
      await page.waitForFunction(marker=>{
        const doc=document.querySelector('iframe[title="输入审计"]').contentDocument;
        const current=[...doc.querySelectorAll('#records tbody tr')].find(record=>record.textContent.includes(marker));
        return !current.querySelector('button').disabled;
      },fixture.keyword+'ENTRY-22');
      assert(await opener.evaluate(button=>button.ownerDocument.activeElement===button),'请求完成后未恢复提前关闭详情的原查看按钮焦点');
    } finally {
      releaseDetail();
      await page.unroute(detailRoute,delayedDetail);
    }
    const actions=await verifyRecordActions(page,frame,opener,detailRoute,query,assert);
    await verifyPlainHTTPCopy(page,fixture,assert);
    return {desktopHeader:true,narrowNoDocumentOverflow:true,lightDarkTheme:true,managementDraftsPreserved:true,managementNoReload:true,timePresets:true,filteredSummary:true,detailEscapeFocus:true,pendingDetailEscapeFocus:true,modalPlainText:true,plainHTTPCopy:true,...actions};
  } finally {
    page.off('request',observe);
    if(await frame.locator('#detail-dialog').isVisible())await frame.locator('#close-detail').click();
    if(await frame.locator('#management-dialog').isVisible())await frame.locator('#close-management').click();
    if(await frame.locator('html').getAttribute('data-theme')!==initialTheme)await frame.locator('#theme-toggle').click();
    await page.setViewportSize(viewport);
  }
};

/** 在真正非安全 HTTP 页验证复制；page/fixture 提供当前实例及记录，assert 校验行为，无返回值，失败抛错。 */
async function verifyPlainHTTPCopy(page,fixture,assert) {
  const origin=new URL(page.url()).origin, src=await page.locator('iframe[title="输入审计"]').evaluate(iframe=>iframe.src);
  const plain=await page.context().newPage(), text=' \t普通 HTTP <b>正文</b>\r\n第二行🙂\n ';
  /** 把普通 HTTP 页的同源资源转发至本轮合成实例；route 为请求，无返回值，网络失败抛错。 */
  const proxy=async route=>{
    const url=new URL(route.request().url());
    const response=await route.fetch({url:origin+url.pathname+url.search});
    if(url.pathname.endsWith('/detail'))await route.fulfill({response,json:{...await response.json(),text}});
    else await route.fulfill({response});
  };
  await plain.route('http://audit-http.test/**',proxy);
  try {
    await page.evaluate(()=>navigator.clipboard.writeText('synthetic-http-clipboard-sentinel'));
    await plain.goto('http://audit-http.test'+new URL(src).pathname);
    assert(await plain.evaluate(()=>!window.isSecureContext && navigator.clipboard===undefined),'普通 HTTP 场景没有真实缺少 Clipboard API');
    await plain.locator('#management-key').fill('synthetic-admin-audit-test-only');await plain.locator('#connect').click();
    await plain.locator('#message').filter({hasText:'管理鉴权通过'}).waitFor();
    await plain.locator('#keyword-filter').fill(fixture.keyword+'ENTRY-22');await plain.locator('#apply-filters').click();
    const opener=plain.locator('#records tbody tr').filter({hasText:fixture.keyword+'ENTRY-22'}).getByRole('button',{name:'查看',exact:true});
    await opener.click();await plain.locator('#copy-detail:not(:disabled)').waitFor();
    await plain.evaluate(()=>{
      document.addEventListener('copy',()=>{
        const source=document.querySelector('#detail-dialog .copy-source');
        window.__auditHTTPSelection=Boolean(source && source.textContent===document.querySelector('#detail').textContent && getSelection().toString()===source.textContent);
      },{once:true});
    });
    await plain.locator('#copy-detail').click();await plain.locator('#copy-message').filter({hasText:'已复制已保存纯文本'}).waitFor();
    assert(await page.evaluate(()=>navigator.clipboard.readText())===text,'真实普通 HTTP 复制没有逐字保存原正文');
    assert(await plain.evaluate(()=>window.__auditHTTPSelection)===true && await plain.locator('.copy-source').count()===0,'真实普通 HTTP 未使用 dialog 内的完整原文选区或遗留载体');
    await plain.keyboard.press('Escape');
    assert(await opener.evaluate(button=>button.ownerDocument.activeElement===button),'普通 HTTP 复制后关闭详情未恢复原查看按钮焦点');
  } finally {await plain.close();}
}

/** 验证详情的请求值、复制与失效状态；参数提供页面、详情入口和查询断言工具，返回有限证据，行为不符抛错。 */
async function verifyRecordActions(page,frame,opener,detailRoute,query,assert) {
  const text=' \t原始正文 <b>不渲染</b>\r\n第二行🙂\n ';
  let body={text,request_reasoning:{type:'enabled',budget_tokens:0}}, status=200;
  /** 用真实详情元数据和合成边界值响应；route 为当前详情请求，无返回值，网络失败抛错。 */
  const detail=async route=>{
    const response=await route.fetch();
    const data=status===200?{...await response.json(),...body}:{error:'synthetic_detail_failure'};
    // 大预算夹具必须发送 JSON number；先用十进制字符串构造，避免测试端已发生 Number 舍入。
    await route.fulfill({response,status,body:JSON.stringify(data).replace(/"budget_tokens":"(\d+)"/g,'"budget_tokens":$1')});
  };
  /** 打开合成详情并等待本次加载结束；无参数或返回值，页面未完成加载时抛错。 */
  async function open() {
    await opener.click();
    await page.waitForFunction(()=>{
      const doc=document.querySelector('iframe[title="输入审计"]').contentDocument;
      return doc.querySelector('#detail-dialog').open && !doc.querySelector('#apply-filters').disabled;
    });
  }
  /** 恢复浏览器真实复制 API；无参数或返回值，页面求值失败时抛错。 */
  async function restoreClipboard() {
    await frame.locator('body').evaluate(element=>{
      const win=element.ownerDocument.defaultView, doc=element.ownerDocument;
      if(win.__auditClipboardDescriptor)Object.defineProperty(win.navigator,'clipboard',win.__auditClipboardDescriptor);
      else delete win.navigator.clipboard;
      if(win.__auditExecDescriptor)Object.defineProperty(doc,'execCommand',win.__auditExecDescriptor);
      else delete doc.execCommand;
      delete win.__auditCopyAttempt;delete win.__auditClipboardDescriptor;delete win.__auditExecDescriptor;
    });
  }
  await page.route(detailRoute,detail);
  try {
    await open();
    await frame.locator('#detail-meta').filter({hasText:'思考设置（请求值）：type=enabled · budget_tokens=0'}).waitFor();
    assert(await frame.locator('#detail').textContent()===text,'详情改变了 API 原字符串的首尾空白或换行');
    await frame.locator('#copy-detail').click();
    await frame.locator('#copy-message').filter({hasText:'已复制已保存纯文本'}).waitFor();
    assert(await page.evaluate(()=>navigator.clipboard.readText())===text,'复制没有逐字保留原详情的空白、换行或 HTML 文本');
    const width=await frame.locator('html').evaluate(root=>({scroll:root.scrollWidth,client:root.clientWidth}));
    assert(width.scroll<=width.client,'390px 下复制按钮导致详情横向溢出');
    // 拒绝权限后不能改用旧命令；错误提示不包含正文或浏览器错误原文。
    await frame.locator('body').evaluate(element=>{
      const win=element.ownerDocument.defaultView, doc=element.ownerDocument;
      win.__auditClipboardDescriptor=Object.getOwnPropertyDescriptor(win.navigator,'clipboard');
      win.__auditExecDescriptor=Object.getOwnPropertyDescriptor(doc,'execCommand');
      win.__auditCopyAttempt=0;
      Object.defineProperty(win.navigator,'clipboard',{configurable:true,value:{writeText:async()=>{throw new DOMException('synthetic_permission_denied','NotAllowedError');}}});
      doc.execCommand=()=>{win.__auditCopyAttempt++;return true;};
    });
    try {
      await frame.locator('#copy-detail').click();
      await frame.locator('#copy-message').filter({hasText:'复制失败，请选中详情正文手工复制。'}).waitFor();
      assert(await frame.locator('body').evaluate(element=>element.ownerDocument.defaultView.__auditCopyAttempt)===0,'权限拒绝后绕过 Clipboard API 使用了旧命令');
      assert(await frame.locator('#copy-message').textContent()==='复制失败，请选中详情正文手工复制。','复制失败提示泄漏正文或异常原文');
    } finally {await restoreClipboard();}
    // 缺 API 时必须用 dialog 内的原文选区，真实复制并读回，不用 mock true 冒充成功。
    await page.evaluate(()=>navigator.clipboard.writeText('synthetic-clipboard-sentinel'));
    await frame.locator('body').evaluate(element=>{
      const win=element.ownerDocument.defaultView, doc=element.ownerDocument, command=doc.execCommand;
      win.__auditClipboardDescriptor=Object.getOwnPropertyDescriptor(win.navigator,'clipboard');
      win.__auditExecDescriptor=Object.getOwnPropertyDescriptor(doc,'execCommand');
      Object.defineProperty(win.navigator,'clipboard',{configurable:true,value:undefined});
      doc.execCommand=function(name){
        const source=doc.querySelector('#detail-dialog .copy-source');
        win.__auditCopyAttempt=Boolean(source && source.textContent===doc.querySelector('#detail').textContent && win.getSelection().toString()===source.textContent);
        return command.call(doc,name);
      };
    });
    try {
      await frame.locator('#copy-detail').click();
      await frame.locator('#copy-message').filter({hasText:'已复制已保存纯文本'}).waitFor();
      assert(await page.evaluate(()=>navigator.clipboard.readText())===text,'缺 Clipboard API 时没有实际复制原字符串');
      assert(await frame.locator('body').evaluate(element=>element.ownerDocument.defaultView.__auditCopyAttempt)===true,'旧复制命令没有使用 dialog 内的完整原文选区');
      assert(await frame.locator('.copy-source').count()===0 && await frame.locator('#copy-detail').evaluate(button=>button.ownerDocument.activeElement===button),'临时复制载体未移除或未归还按钮焦点');
      await frame.locator('body').evaluate(element=>{element.ownerDocument.execCommand=()=>false;});
      await frame.locator('#copy-detail').click();
      await frame.locator('#copy-message').filter({hasText:'复制失败，请选中详情正文手工复制。'}).waitFor();
      assert(await frame.locator('.copy-source').count()===0,'复制命令失败遗留正文载体');
    } finally {await restoreClipboard();}
    await frame.locator('#close-detail').click();
    body={text:'',request_reasoning:null};await open();
    assert(await frame.locator('#detail').textContent()==='' && await frame.locator('#copy-detail').isEnabled(),'成功加载的空字符串被当成详情缺失');
    await frame.locator('#detail-meta').filter({hasText:'思考设置（请求值）：未记录'}).waitFor();
    await frame.locator('#copy-detail').click();
    await frame.locator('#copy-message').filter({hasText:'已复制已保存纯文本'}).waitFor();
    assert(await page.evaluate(()=>navigator.clipboard.readText())==='','空正文未实际清空剪贴板');
    await frame.locator('#close-detail').click();body={text,request_reasoning:{}};await open();
    await frame.locator('#detail-meta').filter({hasText:'思考设置（请求值）：未指定'}).waitFor();
    await frame.locator('#close-detail').click();status=503;await open();
    assert(await frame.locator('#copy-detail').isDisabled(),'详情请求失败仍可复制上一条正文');
    await frame.locator('#detail').filter({hasText:'详情加载失败'}).waitFor();
    await frame.locator('#close-detail').click();
    await query('#refresh');
    assert(await frame.locator('#copy-detail').isDisabled() && await frame.locator('#copy-message').textContent()==='','列表刷新未清除可复制源或旧提示');
    const api=detailRoute.split('/detail?')[0];
    let reasoning;
    /** 为列表加入请求值显示边界，不改正文和记录身份；route 为列表请求，无返回值，网络失败抛错。 */
    const reasoningRecords=async route=>{
      const response=await route.fetch(), data=await response.json();
      data.records=data.records.map(record=>({...record,request_reasoning:reasoning}));
      await route.fulfill({response,body:JSON.stringify(data).replace(/"budget_tokens":"(\d+)"/g,'"budget_tokens":$1')});
    };
    await page.route(api+'/records/search',reasoningRecords);
    try {
      for(const [value,label] of [[undefined,'未记录'],[null,'未记录'],[{},'未指定'],[{effort:'minimal'},'effort=minimal'],[{type:'enabled',budget_tokens:0},'type=enabled · budget_tokens=0'],[{effort:'high',type:'adaptive'},'effort=high · type=adaptive'],[{budget_tokens:'9007199254740993'},'budget_tokens=9007199254740993'],[{budget_tokens:'9223372036854775807'},'budget_tokens=9223372036854775807']]) {
        reasoning=value;await query('#refresh');
        assert(await frame.locator('#records .reasoning').first().textContent()===label,'列表改变思考请求值或丢失零预算/旧记录状态/大整数精度');
        if(typeof value?.budget_tokens==='string') {
          status=200;body={text,request_reasoning:value};await open();
          assert((await frame.locator('#detail-meta').textContent()).endsWith('思考设置（请求值）：'+label),'详情未保持超安全整数范围预算的原始十进制值');
          await frame.locator('#close-detail').click();
        }
      }
    } finally {await page.unroute(api+'/records/search',reasoningRecords);}
    status=200;body={text,request_reasoning:{}};await open();await frame.locator('#close-detail').click();
    /** 模拟服务端撤销管理鉴权；route 为状态查询，无返回值，响应失败抛错。 */
    const revoked=route=>route.fulfill({status:401,json:{error:'synthetic_auth_revoked'}});
    await page.route(api+'/status',revoked);
    try {
      await frame.locator('#refresh').click();await frame.locator('#login').waitFor({state:'visible'});
      assert(await frame.locator('#copy-detail').isDisabled() && await frame.locator('#detail').textContent()==='请选择一条记录。' && await frame.locator('#copy-message').textContent()!=='已复制已保存纯文本。','鉴权撤销未清除详情原文与可复制源');
    } finally {await page.unroute(api+'/status',revoked);}
    await frame.locator('#management-key').fill('synthetic-admin-audit-test-only');
    await frame.locator('#connect').click();
    await frame.locator('#message').filter({hasText:'管理鉴权通过'}).waitFor();
    return {clipboardExactText:true,clipboardFallback:true,clipboardDenied:true,clipboardCommandFailure:true,emptyDetailCopy:true,detailCopyInvalidation:true,detailCopyAuthRevoked:true,requestReasoningDisplay:true,requestReasoningInt64Exact:true};
  } finally {
    if(await frame.locator('#detail-dialog').isVisible())await frame.locator('#close-detail').click();
    await page.unroute(detailRoute,detail);
  }
}
