const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright-core');

const output = process.env.OUTPUT_DIR;
const diagnostics = [];
const record = message => {
  const line = `${new Date().toISOString()} ${message}`;
  diagnostics.push(line);
  fs.writeFileSync(path.join(output, 'browser-diagnostics.txt'), diagnostics.join('\n') + '\n');
  console.log(line);
};

function trendFixture() {
  const calls = [24,31,28,38,42,47,55,63,58,71,84,79,96,102,111,108,126,139,132,118,104,91,77,69];
  const failed = [0,1,0,1,1,0,2,1,0,1,2,1,1,0,2,1,1,2,1,1,0,1,0,1];
  const p95 = [520,560,540,610,590,650,690,720,680,760,820,790,850,890,940,910,980,1040,990,920,860,790,710,660];
  const now = Date.now();
  return {
    range_seconds: 86400,
    bucket_seconds: 3600,
    retention_seconds: 604800,
    points: calls.map((requests, index) => ({
      time: new Date(now - (calls.length - 1 - index) * 3600000).toISOString(),
      requests,
      failed: failed[index],
      p95_latency_ms: p95[index],
    })),
  };
}

let browser;

(async () => {
  browser = await chromium.launch({
    headless: true,
    executablePath: process.env.CHROME,
    args: ['--no-sandbox', '--disable-dev-shm-usage', '--font-render-hinting=none'],
  });
  try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1000 },
    deviceScaleFactor: 1,
    locale: 'zh-CN',
    colorScheme: 'light',
    reducedMotion: 'reduce',
  });
  const page = await context.newPage();
  page.on('console', message => record(`[console:${message.type()}] ${message.text()}`));
  page.on('pageerror', error => record(`[pageerror] ${error.stack || error.message}`));
  page.on('requestfailed', request => record(`[requestfailed] ${request.method()} ${request.url()} ${request.failure()?.errorText || ''}`));
  page.on('response', response => {
    if (response.url().includes('/admin/api/') && response.status() >= 400) {
      record(`[admin-response] ${response.status()} ${response.url()}`);
    }
  });

  await page.route('**/admin/api/trends?*', async route => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(trendFixture()) });
  });

  const login = await context.request.post('http://127.0.0.1:45679/admin/api/login', { data: { token: 'preview-admin-token' } });
  const loginBody = await login.text();
  if (!login.ok()) throw new Error(`admin login failed: ${login.status()} ${loginBody}`);
  const loginData = JSON.parse(loginBody);

  record('[stage] open usage and quota');
  await page.goto('http://127.0.0.1:45679/admin', { waitUntil: 'domcontentloaded', timeout: 45000 });
  await page.waitForSelector('#view-monitor.active .v10-kpi-strip', { state: 'visible', timeout: 30000 });
  await page.waitForFunction(() => document.querySelectorAll('#v10QuotaBoard .v10-quota-account').length >= 5, null, { timeout: 30000 });
  await page.waitForFunction(() => document.querySelector('#v10CallCount')?.textContent !== '—', null, { timeout: 30000 });
  await page.waitForTimeout(1000);
  await page.screenshot({ path: path.join(output, 'v10-usage-before-quality.png'), fullPage: false, animations: 'disabled' });

  record('[stage] run three-probe quality tests');
  await page.locator('#v10TestAllChannels').click();
  await page.waitForFunction(() => {
    const rows = [...document.querySelectorAll('#v10QualityRows .v10-quality-row')];
    return rows.length >= 4 && rows.every(row => !row.classList.contains('is-running') && !row.innerText.includes('未测试'));
  }, null, { timeout: 90000 });
  await page.screenshot({ path: path.join(output, 'v10-usage-quality-results.png'), fullPage: false, animations: 'disabled' });
  await page.screenshot({ path: path.join(output, 'v10-usage-full.png'), fullPage: true, animations: 'disabled' });

  record('[stage] inspect quota account inventory');
  await page.locator('button[data-view="accounts"]').click();
  await page.waitForSelector('#view-accounts.active .v10-account-workspace', { state: 'visible', timeout: 20000 });
  await page.waitForFunction(() => document.querySelectorAll('#oauthAccounts .channel-account').length >= 5, null, { timeout: 30000 });
  await page.screenshot({ path: path.join(output, 'v10-accounts.png'), fullPage: false, animations: 'disabled' });
  await page.screenshot({ path: path.join(output, 'v10-accounts-full.png'), fullPage: true, animations: 'disabled' });

  record('[stage] traverse every provider onboarding branch');
  await page.getByRole('button', { name: '添加账号' }).click();
  await page.waitForSelector('#quickAuthDialog[open] #v10ProviderGrid', { state: 'visible', timeout: 10000 });
  const providerButtons = await page.locator('#v10ProviderGrid [data-v10-provider]').all();
  const providerChecks = [];
  for (const providerButton of providerButtons) {
    const provider = await providerButton.getAttribute('data-v10-provider');
    await providerButton.click();
    const methods = await page.locator('#v10MethodGrid .v10-method-card').count();
    const summary = await page.locator('#v10ProviderSummary').innerText();
    if (!methods || !summary.trim()) throw new Error(`provider ${provider} has no onboarding methods`);
    providerChecks.push({ provider, methods, summary });
  }
  fs.writeFileSync(path.join(output, 'provider-onboarding-checks.json'), JSON.stringify(providerChecks, null, 2));
  await page.locator('[data-v10-provider="anthropic"]').click();
  await page.screenshot({ path: path.join(output, 'v10-add-account-claude.png'), fullPage: false, animations: 'disabled' });
  await page.locator('[data-v10-provider="gemini"]').click();
  await page.screenshot({ path: path.join(output, 'v10-add-account-gemini.png'), fullPage: false, animations: 'disabled' });

  record('[stage] test an unsaved connection');
  await page.locator('[data-v10-provider="deepseek"]').click();
  await page.getByRole('button', { name: 'DeepSeek API Key' }).click();
  await page.waitForSelector('#accountDialog[open] #v10TestAccountBtn', { state: 'visible', timeout: 10000 });
  await page.locator('#accountForm input[name="name"]').fill('Preview Local API');
  await page.locator('#accountForm input[name="base_url"]').fill('http://127.0.0.1:45678/v1');
  await page.locator('#accountForm input[name="api_key"]').fill('preview');
  await page.locator('#v10TestAccountBtn').click();
  await page.waitForFunction(() => document.querySelector('#v10AccountTestResult')?.classList.contains('good'), null, { timeout: 20000 });
  await page.screenshot({ path: path.join(output, 'v10-manual-account-tested.png'), fullPage: false, animations: 'disabled' });

  record('[stage] execute a real import dry-run');
  await page.locator('#accountDialog .close-btn').click();
  await page.getByRole('button', { name: '批量导入' }).click();
  await page.waitForSelector('#importDialog[open] #importFileInput', { state: 'attached', timeout: 10000 });
  const importPayload = {
    type: 'lite2api-data',
    version: 1,
    accounts: [{
      id: 'import-preview',
      name: 'Imported Preview',
      type: 'openai',
      base_url: 'http://127.0.0.1:45678/v1',
      api_key: 'preview-import-key',
      models: ['gpt-5.6-codex-fast'],
      concurrency: 2,
      priority: 0,
      weight: 1,
      enabled: true,
    }],
    proxies: [],
  };
  await page.locator('#importFileInput').setInputFiles({
    name: 'lite2api-preview-import.json',
    mimeType: 'application/json',
    buffer: Buffer.from(JSON.stringify(importPayload)),
  });
  await page.locator('#previewImportBtn').click();
  await page.waitForFunction(() => {
    const result = document.querySelector('#importResult');
    return result && !result.hidden && document.querySelector('#resultCreated')?.textContent === '1';
  }, null, { timeout: 20000 });
  await page.screenshot({ path: path.join(output, 'v10-import-dry-run.png'), fullPage: false, animations: 'disabled' });

  record('[stage] verify account test endpoint contract');
  const accountTest = await context.request.post('http://127.0.0.1:45679/admin/api/accounts/test', {
    headers: { 'X-CSRF-Token': loginData.csrf },
    data: {
      account: {
        id: 'api-contract-test',
        name: 'API Contract Test',
        type: 'openai',
        base_url: 'http://127.0.0.1:45678/v1',
        api_key: 'preview',
        auth_header: 'Authorization',
        auth_scheme: 'Bearer',
        concurrency: 1,
        weight: 1,
        enabled: true,
      },
    },
  });
  const accountTestBody = await accountTest.text();
  fs.writeFileSync(path.join(output, 'account-test-response.json'), accountTestBody);
  if (!accountTest.ok()) throw new Error(`account test endpoint failed: ${accountTest.status()} ${accountTestBody}`);

  fs.writeFileSync(path.join(output, 'rendered-admin.html'), await page.content());
  record('[result] every Native v10 product workflow passed');
  } finally {
    await browser?.close();
  }
})().catch(error => {
  record(`[fatal] ${error.stack || error.message || error}`);
  console.error(error.stack || error);
  process.exitCode = 1;
});
