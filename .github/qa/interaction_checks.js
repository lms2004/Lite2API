'use strict';
const assert = require('node:assert/strict');

module.exports = async function checkInteractions(page, log) {
  log('[stage] focused controls, lazy rendering, and refresh lifecycle');
  await page.setViewportSize({width:1440,height:1000});
  await page.locator('.sidebar [data-view="usage"]').click();
  await page.waitForFunction(() => document.querySelector('#callsKpi').textContent !== '—');
  assert.equal(await page.locator('#requestRows tr').count(),0,'collapsed request history should not build hidden rows');
  await page.locator('#requestDisclosure > summary').click();
  await page.waitForSelector('#requestRows tr');
  assert.ok(await page.locator('#requestRows tr').count() <= 30,'request rendering is bounded to a page');
  if (!await page.locator('#requestNext').isDisabled()) {
    const firstRow = await page.locator('#requestRows tr').first().textContent();
    await page.locator('#requestNext').click();
    assert.match(await page.locator('#requestPageInfo').textContent(),/^2 \/ /);
    assert.notEqual(await page.locator('#requestRows tr').first().textContent(),firstRow);
    await page.locator('#requestPrevious').click();
  }
  await page.locator('#requestSearch').fill('no-such-request-fixture');
  await page.waitForFunction(() => document.querySelector('#requestRows').textContent.includes('没有匹配'));
  assert.equal(await page.locator('#requestSearch').inputValue(),'no-such-request-fixture');
  await page.locator('#requestSearch').fill('');
  await page.locator('#requestDisclosure > summary').click();
  const quotaDetails = page.locator('#quotaBoard .quota-details').first();
  await quotaDetails.locator(':scope > summary').click();
  await page.locator('#refreshButton').click();
  await page.waitForFunction(() => !document.querySelector('#refreshButton').disabled);
  assert.equal(await quotaDetails.evaluate(node=>node.open),true,'quota disclosure survives refresh');
  await quotaDetails.locator(':scope > summary').click();

  await page.locator('#usageMetric').selectOption('quota');
  const credential = await page.locator('#usageAccountFilter option').nth(1).getAttribute('value');
  await page.locator('#usageAccountFilter').selectOption(credential);
  assert.ok(!((await page.locator('#chartDataSummary').textContent()).startsWith('0 ')),'quota chart must filter by credential identity');
  await page.locator('#usageMetric').selectOption('requests');
  assert.equal(await page.locator('#usageAccountFilter').inputValue(),'','switching metric families clears incompatible filters');

  let pendingRange;
  const requested = new Promise(resolve => {pendingRange = resolve;});
  await page.route('**/admin/api/trends?range=3d',async route => {
    pendingRange();
    await new Promise(resolve => setTimeout(resolve,350));
    await route.fulfill({json:{points:[{time:new Date().toISOString(),requests:3,failed:0}]}}).catch(() => {});
  });
  await page.route('**/admin/api/trends?range=7d',route => route.fulfill({json:{points:[{time:new Date().toISOString(),requests:777,failed:0}]}}));
  await page.locator('[data-range="3d"]').click();
  await requested;
  await page.locator('[data-range="7d"]').click();
  await page.waitForFunction(() => document.querySelector('#callsKpi').textContent==='777');
  await page.waitForTimeout(400);
  assert.equal(await page.locator('#callsKpi').textContent(),'777','old range must not overwrite newer data');
  await page.unroute('**/admin/api/trends?range=3d');
  await page.unroute('**/admin/api/trends?range=7d');

  await page.locator('.sidebar [data-view="accounts"]').click();
  await page.waitForFunction(() => !document.querySelector('#refreshButton').disabled);
  const menu = page.locator('#authAccounts .compact-row-actions').first();
  await menu.locator('summary').click();
  const priority = menu.locator('[data-oauth-priority]');
  await priority.fill('357');
  const editedNode = await priority.elementHandle();
  const editedID = await menu.evaluate(node => node.closest('[data-oauth-id]').dataset.oauthId);
  let updatedSuccess;
  await page.route('**/admin/api/oauth/accounts',async route => {
    const response = await route.fetch();
    const json = await response.json();
    const account = json.data.find(item => item.id === editedID);
    updatedSuccess = account.success = (account.success || 0)+37;
    await route.fulfill({response,json});
  });
  const stateResponse = page.waitForResponse(response => response.url().endsWith('/admin/api/state'));
  await page.evaluate(() => document.querySelector('#refreshButton').dispatchEvent(new Event('click')));
  await stateResponse;
  await page.waitForFunction(() => !document.querySelector('#refreshButton').disabled);
  await page.unroute('**/admin/api/oauth/accounts');
  await page.waitForFunction(({id,success}) => {
    const row = [...document.querySelectorAll('[data-oauth-id]')].find(node => node.dataset.oauthId === id);
    return Number(row.querySelector('.numeric').firstChild.textContent.replaceAll(',','')) === success;
  },{id:editedID,success:updatedSuccess});
  assert.equal(await priority.inputValue(),'357','background refresh must preserve edited priority');
  assert.equal(await editedNode.evaluate(node => node === document.activeElement),true,'refresh retains the actual focused control');
  assert.equal(await menu.evaluate(node => Number(node.closest('article').querySelector('.numeric').firstChild.textContent.replaceAll(',',''))),updatedSuccess,
    'live account counters update while a priority edit is in progress');
  assert.equal(await menu.evaluate(node => node.open),true,'refresh must not dismiss account management');
  await priority.press('Escape');
  assert.equal(await menu.evaluate(node => node.open),false);
  assert.equal(await menu.locator('summary').evaluate(node => node===document.activeElement),true);

  await page.locator('#openAddAccount').click();
  await page.waitForSelector('#addAccountDialog[open]');
  assert.equal(await page.evaluate(() => document.body.classList.contains('dialog-open')),true);
  await page.keyboard.press('Escape');
  await page.waitForSelector('#addAccountDialog[open]',{state:'hidden'});
  assert.equal(await page.locator('#openAddAccount').evaluate(node => node===document.activeElement),true);
  await page.emulateMedia({reducedMotion:'no-preference'});
  await page.locator('#openAddAccount').click();
  assert.equal(await page.locator('#addAccountDialog').evaluate(node => getComputedStyle(node).animationName),'dialog-enter');
  await page.evaluate(() => {
    document.querySelector('#addAccountDialog .close-button').click();
    document.querySelector('#openAddAccount').click();
  });
  await page.keyboard.press('Escape');
  await page.waitForSelector('#addAccountDialog[open]',{state:'hidden'});
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.locator('.sidebar [data-view="routes"]').click();
  await page.locator('#routeSearch').fill('no-such-route-fixture');
  await page.waitForFunction(() => document.querySelector('#routeList').textContent.includes('没有匹配'));
  await page.locator('#routeSearch').fill('');
  await page.waitForSelector('#routeList [data-route-alias]');
  const alias = await page.locator('#routeAliasInput').inputValue();
  await page.locator('#routeEditor [data-route-action="delete"]').click();
  await page.waitForSelector('#confirmDialog[open]');
  assert.equal(await page.locator('#confirmTitle').textContent(),'删除模型路由');
  assert.equal(await page.locator('#confirmDialog [autofocus]').evaluate(node=>node===document.activeElement),true,'destructive confirmation initially focuses cancel');
  await page.screenshot({path:process.env.OUTPUT_DIR+'/confirm.png'});
  await page.keyboard.press('Escape');
  await page.waitForSelector('#confirmDialog[open]',{state:'hidden'});
  assert.equal(await page.locator('#routeAliasInput').inputValue(),alias,'cancel leaves the route intact');
  assert.equal(await page.locator('#routeEditor [data-route-action="delete"]').evaluate(node=>node===document.activeElement),true);

  await require('./editor_checks')(page,log);

  log('[stage] duplicate submit protection and key revocation');
  await page.locator('.sidebar [data-view="clients"]').click();
  await page.waitForFunction(() => !document.querySelector('#refreshButton').disabled);
  const keysRefreshed = page.waitForResponse(response => response.url().endsWith('/admin/api/client-keys'));
  await page.locator('#refreshButton').click();
  await keysRefreshed;
  await page.locator('#openCreateKey').click();
  await page.locator('#createKeyForm [name="name"]').fill('interaction-check');
  let creates=0;
  const countCreates=request=>{if(request.method()==='POST'&&request.url().endsWith('/admin/api/client-keys'))creates++;};
  page.on('request',countCreates);
  await page.evaluate(()=>{const form=document.querySelector('#createKeyForm');form.requestSubmit();form.requestSubmit();});
  await page.waitForSelector('#createKeyDialog[open]',{state:'hidden'});
  const key = page.locator('#clientKeyRows article').filter({hasText:'interaction-check'});
  await key.waitFor();
  assert.equal(creates,1,'repeated submission must create exactly one key');
  page.off('request',countCreates);
  await key.locator('[data-key-delete]').click();
  await page.locator('#confirmAccept').click();
  await key.waitFor({state:'detached'});
  assert.equal(await page.locator('#createdSecret').isHidden(),true,'revoked keys must not remain offered as active configuration');
  assert.equal(await page.locator('#createdSecretValue').inputValue(),'');

  await page.locator('.sidebar [data-view="accounts"]').click();
  await page.locator('[data-account-tab="connections"]').click();
  assert.equal(await page.locator('#connectionTitle').evaluate(node=>node===document.activeElement),true,'connection jump moves focus to its heading');
  assert.ok((await page.locator('#connectionTitle').boundingBox()).y < 400,'connection jump exposes the working section');

  // Fake time only after all stateful workflows complete. A hidden page must not
  // poll; restoring visibility resumes exactly one active refresh generation.
  await page.locator('.sidebar [data-view="clients"]').click();
  await page.waitForFunction(() => !document.querySelector('#refreshButton').disabled);
  await page.clock.install();
  let backgroundRequests = 0;
  const count = request => {if(request.url().includes('/admin/api/')) backgroundRequests++;};
  page.on('request',count);
  await page.evaluate(() => {
    Object.defineProperty(document,'hidden',{configurable:true,get:()=>true});
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await page.clock.fastForward(120000);
  assert.equal(backgroundRequests,0,'hidden page must stop all refresh polling');
  const resumed = page.waitForResponse(response => response.url().endsWith('/admin/api/state'));
  await page.evaluate(() => {
    delete document.hidden;
    document.dispatchEvent(new Event('visibilitychange'));
  });
  await resumed;
  page.off('request',count);
  await page.clock.resume();

  log('[stage] responsive layouts and menu boundaries');
  for (const width of [1440,1024,768,390,320]) {
    await page.setViewportSize({width,height:900});
    for (const view of ['usage','accounts','routes','clients','diagnostics']) {
      await page.locator(`.sidebar [data-view="${view}"]`).click();
      const overflow = await page.evaluate(() => ({width:innerWidth,scroll:document.documentElement.scrollWidth,
        elements:[...document.querySelectorAll('main *')].filter(node => {
          const box=node.getBoundingClientRect();return box.width>0&&box.right>innerWidth+1&&!node.closest('.table-scroll,.route-list,pre');
        }).slice(0,8).map(node=>({tag:node.tagName,class:node.className,right:node.getBoundingClientRect().right}))}));
      assert.ok(overflow.scroll<=width,`${view} overflows ${width}px: ${JSON.stringify(overflow)}`);
    }
    await page.locator('.sidebar [data-view="accounts"]').click();
    const menu = page.locator('#authAccounts .compact-row-actions').first();
    await menu.locator('summary').click();
    const box=await menu.locator(':scope > div').boundingBox();
    assert.ok(box.x>=0&&box.x+box.width<=width+1&&box.y>=0,`account menu must fit ${width}px viewport: ${JSON.stringify(box)}`);
    await page.keyboard.press('Escape');
  }
  await page.setViewportSize({width:390,height:900});
  await page.locator('.sidebar [data-view="usage"]').click();
  await page.evaluate(() => {document.documentElement.style.fontSize='200%';});
  assert.ok(await page.evaluate(() => document.documentElement.scrollWidth<=innerWidth),'text enlargement must not clip the overview');
  await page.evaluate(() => {document.documentElement.style.fontSize='';});
};
