'use strict';

const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright-core');

const output = process.env.OUTPUT_DIR;
const adminURL = (process.env.ADMIN_URL || 'http://127.0.0.1:45679/admin').replace(/\/$/, '');
const testModelBaseURL = process.env.TEST_MODEL_BASE_URL || 'http://127.0.0.1:45678/v1';
const diagnostics = [];
const browserErrors = [];
let expectedQualityFailure = false;

function log(message) {
  const line = `${new Date().toISOString()} ${message}`;
  diagnostics.push(line);
  fs.writeFileSync(path.join(output, 'browser-diagnostics.txt'), `${diagnostics.join('\n')}\n`);
  console.log(line);
}

function trendFixture() {
  const calls = [24, 31, 28, 38, 42, 47, 55, 63, 58, 71, 84, 79, 96, 102, 111, 108, 126, 139, 132, 118, 104, 91, 77, 69];
  const failed = [0, 1, 0, 1, 1, 0, 2, 1, 0, 1, 2, 1, 1, 0, 2, 1, 1, 2, 1, 1, 0, 1, 0, 1];
  const p95 = [520, 560, 540, 610, 590, 650, 690, 720, 680, 760, 820, 790, 850, 890, 940, 910, 980, 1040, 990, 920, 860, 790, 710, 660];
  const current = Date.now();
  return {
    range_seconds: 86400,
    bucket_seconds: 3600,
    retention_seconds: 604800,
    points: calls.map((requests, index) => ({
      time: new Date(current - (calls.length - 1 - index) * 3600000).toISOString(),
      requests,
      failed: failed[index],
      p95_latency_ms: p95[index]
    }))
  };
}

async function screenshot(page, name) {
  await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: false, animations: 'disabled' });
}

async function waitForToast(page, text) {
  await page.waitForFunction(expected => document.querySelector('#toast')?.textContent.includes(expected), text);
}

let browser;
(async () => {
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROME,
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--font-render-hinting=none']
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    deviceScaleFactor: 1,
    locale: 'zh-CN',
    colorScheme: 'light',
    reducedMotion: 'reduce'
  });
  const page = await context.newPage();
  page.on('console', message => {
    log(`[console:${message.type()}] ${message.text()}`);
    const expected503 = expectedQualityFailure && message.text().includes('503');
    if (message.type() === 'error' && !expected503) browserErrors.push(message.text());
  });
  page.on('pageerror', error => {
    browserErrors.push(error.stack || error.message);
    log(`[pageerror] ${error.stack || error.message}`);
  });
  page.on('requestfailed', request => log(`[requestfailed] ${request.method()} ${request.url()} ${request.failure()?.errorText || ''}`));
  await page.route('**/admin/api/trends?*', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(trendFixture())
  }));

  const login = await context.request.post(`${adminURL}/api/login`, { data: { token: 'preview-admin-token' } });
  if (!login.ok()) throw new Error(`login failed ${login.status()} ${await login.text()}`);

  log('[stage] usage and quality');
  await page.goto(adminURL, { waitUntil: 'domcontentloaded', timeout: 45000 });
  await page.waitForSelector('[data-ui="canonical-usage-console"] #view-usage.active', { state: 'visible' });
  await page.waitForFunction(() => document.querySelectorAll('#quotaBoard .quota-account').length >= 5, null, { timeout: 30000 });
  await page.waitForFunction(() => document.querySelector('#callsKpi')?.textContent !== '—');
  await page.waitForTimeout(500);
  await screenshot(page, 'usage');
  expectedQualityFailure = true;
  await page.locator('#testAllChannels').click();
  await page.waitForFunction(() => {
    const runs = [...document.querySelectorAll('#qualityRows .quality-run')];
    return runs.length >= 4 && runs.every(run => run.querySelectorAll('i.good,i.bad').length === 3);
  }, null, { timeout: 90000 });
  expectedQualityFailure = false;
  await screenshot(page, 'quality-results');

  log('[stage] credential pool semantics');
  await page.locator('[data-view="accounts"]').first().click();
  await page.waitForSelector('#view-accounts.active');
  await page.waitForFunction(() => document.querySelectorAll('#authAccounts .auth-account').length >= 5);
  if (await page.locator('[data-quick-oauth]').count() !== 4) throw new Error('common OAuth providers are not available as direct actions');
  if (await page.locator('#connectionPane').isHidden()) throw new Error('API connections still require a separate tab interaction');
  if (!(await page.locator('#authAccountToolbar').isHidden())) throw new Error('small personal account pool should not show redundant filters');
  await page.locator('#poolSettings summary').click();
  const routingHint = await page.locator('#oauthRoutingHint').innerText();
  if (!routingHint.includes('大')) throw new Error(`OAuth priority direction is unclear: ${routingHint}`);
  await page.locator('#oauthRoutingStrategy').selectOption('fill-first');
  await waitForToast(page, '选号策略已更新');
  await page.locator('#oauthRoutingStrategy').selectOption('round-robin');
  await waitForToast(page, '选号策略已更新');
  await page.locator('#poolSettings summary').click();
  await screenshot(page, 'accounts');

  log('[stage] direct channel chat');
  await page.locator('[data-action="connection-chat"][data-id="fast-lane"]').click();
  await page.waitForSelector('#channelChatDialog[open]');
  if ((await page.locator('#channelChatAccount').inputValue()) !== 'fast-lane') throw new Error('channel chat did not retain the selected connection');
  if ((await page.locator('#channelChatModel').inputValue()) !== 'gpt-5.6-codex-fast') throw new Error('channel chat did not select the channel model');
  await page.locator('#channelChatInput').fill('请直接回复当前渠道名称');
  await page.locator('#sendChannelChat').click();
  await page.waitForFunction(() => [...document.querySelectorAll('#channelChatMessages .channel-chat-message.assistant')].some(node => node.textContent.includes('Codex Fast Lane response')), null, { timeout: 30000 });
  const channelChatText = await page.locator('#channelChatMessages').innerText();
  for (const expected of ['gpt-5.6-codex-fast', 'Token', '请求']) {
    if (!channelChatText.includes(expected)) throw new Error(`channel chat metadata is missing ${expected}`);
  }
  await screenshot(page, 'channel-chat');
  await page.locator('#channelChatDialog .close-button').click();

  log('[stage] every onboarding provider branch');
  await page.locator('#openAddAccount').click();
  await page.waitForSelector('#addAccountDialog[open] #providerStep', { state: 'visible' });
  const providerKeys = await page.locator('#providerList [data-provider]').evaluateAll(nodes => nodes.map(node => node.dataset.provider));
  const branches = [];
  for (const key of providerKeys) {
    await page.locator(`#providerList [data-provider="${key}"]`).click();
    await page.waitForSelector('#methodStep', { state: 'visible' });
    const methods = await page.locator('#providerMethods .method-card').count();
    const summary = (await page.locator('#providerSummary').innerText()).trim();
    if (!methods || !summary) throw new Error(`provider ${key} is incomplete`);
    branches.push({ provider: key, methods, summary });
    await page.locator('#onboardingBack').click();
    await page.waitForSelector('#providerStep', { state: 'visible' });
  }
  fs.writeFileSync(path.join(output, 'provider-branches.json'), JSON.stringify(branches, null, 2));
  await page.locator('#providerList [data-provider="anthropic"]').click();
  await screenshot(page, 'add-upstream-anthropic');
  await page.locator('#onboardingBack').click();
  await page.locator('#providerList [data-provider="gemini"]').click();
  await screenshot(page, 'add-upstream-gemini');
  await page.locator('#onboardingBack').click();

  log('[stage] automatic connection test/save and immediate route activation');
  await page.locator('#providerList [data-provider="deepseek"]').click();
  await page.getByRole('button', { name: 'DeepSeek API Key' }).click();
  await page.waitForSelector('#manualAccountDialog[open]');
  await page.locator('#manualAccountForm input[name="name"]').fill('Preview Local API');
  await page.locator('#manualAccountForm input[name="base_url"]').fill(testModelBaseURL);
  await page.locator('#manualAccountForm input[name="api_key"]').fill('preview');
  await page.locator('#manualAccountForm button[type="submit"]').click();
  await page.waitForSelector('#routeCreateDialog[open]', { timeout: 30000 });
  await page.waitForFunction(() => document.querySelector('#connectionTestResult')?.classList.contains('good'), null, { timeout: 20000 });
  const discoveredModels = await page.locator('#manualAccountForm input[name="models"]').inputValue();
  if (!discoveredModels.includes('gpt-5.6-codex-fast')) throw new Error(`discovered model was not adopted: ${discoveredModels}`);
  if ((await page.locator('#routeCreateConnection').inputValue()) !== 'deepseek-main') throw new Error('route handoff lost the created connection');
  await screenshot(page, 'connection-saved-route-ready');
  await page.locator('#routeCreateForm button[type="submit"]').click();
  await page.waitForSelector('#view-routes.active');
  await page.waitForFunction(() => document.querySelector('#routeStatus')?.textContent === '已同步', null, { timeout: 20000 });
  if (await page.locator('#routeStatus').isVisible()) throw new Error('synced route status should not occupy the primary action area');
  if (!(await page.locator('#routeList [data-route-alias="gpt-5.6-codex-fast"]').count())) throw new Error('new route was not persisted immediately');
  await page.locator('#routeEditor [data-route-action="add-target"]').click();
  await page.waitForFunction(() => document.querySelectorAll('#routeEditor .target-row').length === 2);
  if (!(await page.locator('#routeEditor .route-editor-error').count())) throw new Error('blank fallback target was not validated');
  await page.locator('#routeEditor .target-row').last().locator('[data-target-action="delete"]').click();
  await page.waitForFunction(() => document.querySelectorAll('#routeEditor .target-row').length === 1);
  await page.locator('#routeEditor .route-target-picker summary').click();
  await page.locator('#routeEditor [data-route-account-toggle="fast-lane"]').check();
  await page.waitForFunction(() => document.querySelectorAll('#routeEditor .target-row').length === 2);
  if (!(await page.locator('#routeEditor .route-target-picker').evaluate(element => element.open))) throw new Error('multi-target picker did not preserve its open state');
  await page.locator('#routeEditor [data-route-account-toggle="fast-lane"]').uncheck();
  await page.waitForFunction(() => document.querySelectorAll('#routeEditor .target-row').length === 1);
  await page.locator('#routeEditor .route-advanced-settings summary').click();
  await page.locator('#routeStrategyInput').selectOption('round_robin');
  await page.waitForFunction(() => document.querySelector('#routeStatus')?.textContent.includes('未保存'));
  await page.locator('#routeEditor [data-route-action="duplicate"]').click();
  await page.waitForFunction(() => document.querySelector('#routeAliasInput')?.value.includes('-copy'));
  page.once('dialog', dialog => dialog.accept());
  await page.locator('#routeEditor [data-route-action="delete"]').click();
  await page.waitForFunction(() => ![...document.querySelectorAll('#routeList [data-route-alias]')].some(button => button.dataset.routeAlias.includes('-copy')));
  await screenshot(page, 'route-draft');
  await page.locator('#saveRoutesButton').click();
  await page.waitForFunction(() => document.querySelector('#routeStatus')?.textContent === '已同步', null, { timeout: 20000 });
  await screenshot(page, 'routes-saved');

  log('[stage] import dry-run');
  await page.locator('[data-view="accounts"]').first().click();
  await page.locator('#openImport').click();
  await page.waitForSelector('#importDialog[open]');
  const payload = {
    type: 'lite2api-data',
    version: 1,
    accounts: [{
      id: 'import-preview',
      name: 'Imported Preview',
      type: 'openai',
      base_url: testModelBaseURL,
      api_key: 'preview-import-key',
      models: ['gpt-5.6-codex-fast'],
      concurrency: 2,
      priority: 0,
      weight: 1,
      enabled: false
    }],
    proxies: []
  };
  await page.locator('#importFilesInput').setInputFiles({
    name: 'preview.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify(payload))
  });
  await page.locator('#previewImport').click();
  await page.waitForFunction(() => document.querySelector('#importTotals')?.innerText.includes('新增 1'), null, { timeout: 20000 });
  await screenshot(page, 'import-dry-run');
  await page.locator('#importDialog .close-button').click();

  log('[stage] mobile layout and structural checks');
  const duplicateIDs = await page.evaluate(() => {
    const counts = new Map();
    for (const node of document.querySelectorAll('[id]')) counts.set(node.id, (counts.get(node.id) || 0) + 1);
    return [...counts.entries()].filter(([, count]) => count > 1);
  });
  if (duplicateIDs.length) throw new Error(`duplicate IDs: ${JSON.stringify(duplicateIDs)}`);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('[data-view="routes"]').first().click();
  await screenshot(page, 'routes-mobile');
  await page.locator('[data-view="accounts"]').first().click();
  await screenshot(page, 'accounts-mobile');
  fs.writeFileSync(path.join(output, 'rendered-admin.html'), await page.content());

  if (browserErrors.length) throw new Error(`browser emitted errors:\n${browserErrors.join('\n')}`);
  log('[result] canonical product workflow passed');
})().catch(error => {
  log(`[fatal] ${error.stack || error.message}`);
  console.error(error);
  process.exitCode = 1;
}).finally(async () => {
  await browser?.close();
});
