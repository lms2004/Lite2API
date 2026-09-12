'use strict';
const assert = require('node:assert/strict');

module.exports = async function checkEditor(page, log) {
  log('[stage] continuous route editing and saves during further edits');
  const editor = page.locator('#routeEditor');
  const strategy = page.locator('#routeStrategyInput');
  const advanced = editor.locator('.route-advanced-settings');
  if (!(await advanced.evaluate((node) => node.open))) await advanced.locator(':scope > summary').click();
  const originalStrategy = await strategy.inputValue();
  const firstStrategy = originalStrategy === 'round_robin' ? 'least_loaded' : 'round_robin';
  const originalAlias = await page.locator('#routeAliasInput').inputValue();
  const originalStrategyNode = await strategy.elementHandle();
  await strategy.focus();
  await strategy.selectOption(firstStrategy);
  assert.equal(
    await originalStrategyNode.evaluate((node) => node === document.activeElement),
    true,
    'changing strategy keeps the same focused control',
  );
  await strategy.selectOption(originalStrategy);
  assert.equal(
    await page.locator('#routeChangeBar').isHidden(),
    true,
    'returning to the saved value clears a semantic no-op draft',
  );
  await strategy.selectOption(firstStrategy);

  const alias = page.locator('#routeAliasInput');
  const originalAliasNode = await alias.elementHandle();
  await alias.fill(originalAlias + '-editing-check');
  await alias.press('Tab');
  assert.equal(
    await originalAliasNode.evaluate((node) => node === document.querySelector('#routeAliasInput')),
    true,
  );
  assert.equal(
    await advanced.locator(':scope > summary').evaluate((node) => node === document.activeElement),
    true,
    'Tab after editing an alias reaches the next control',
  );

  const otherRoute = page.locator('#routeList [data-route-alias]').nth(1);
  const otherAlias = await otherRoute.getAttribute('data-route-alias');
  const continuedAlias = originalAlias + '-editing-continued';
  await alias.fill(continuedAlias);
  await otherRoute.click();
  await page.waitForFunction(
    (expected) => document.querySelector('#routeAliasInput').value === expected,
    otherAlias,
  );
  await page.locator(`#routeList [data-route-alias="${continuedAlias}"]`).click();
  assert.equal(
    await alias.inputValue(),
    continuedAlias,
    'editing then switching routes works with one click',
  );

  const firstTarget = editor.locator('[data-target-index="0"] [data-target-field="account"]');
  const firstAccount = await firstTarget.inputValue();
  const originalTargetNode = await firstTarget.elementHandle();
  await firstTarget.focus();
  await firstTarget.selectOption('');
  await editor.locator('.route-editor-error').waitFor();
  assert.equal(
    await originalTargetNode.evaluate((node) => node === document.activeElement),
    true,
    'inserting validation feedback must not move the input',
  );
  await firstTarget.selectOption(firstAccount);
  assert.equal(
    await originalTargetNode.evaluate((node) => node === document.activeElement),
    true,
    'removing validation feedback must not move the input',
  );

  const picker = editor.locator('.route-target-picker');
  await picker.locator('summary').click();
  const choice = picker.locator('[data-route-account-toggle]:checked').first();
  const accountID = await choice.getAttribute('data-route-account-toggle');
  const originalChoice = await choice.elementHandle();
  await choice.uncheck();
  assert.equal(await originalChoice.evaluate((node) => node === document.activeElement), true);
  assert.equal(
    await picker.evaluate((node) => node.open),
    true,
    'choosing an upstream keeps the picker open',
  );
  await picker.locator(`[data-route-account-toggle="${accountID}"]`).check();
  await page.keyboard.press('Escape');

  const count = await editor.locator('.target-row').count();
  await editor.locator('[data-route-action="add-target"]').click();
  assert.equal(
    await editor
      .locator('.target-row:last-child [data-target-field="account"]')
      .evaluate((node) => node === document.activeElement),
    true,
    'adding an upstream focuses the new connection selector',
  );
  await editor.locator('[data-target-index="0"] [data-target-action="down"]').click();
  assert.equal(
    await editor.locator('[data-target-index="1"] [data-target-field="account"]').inputValue(),
    firstAccount,
  );
  assert.equal(
    await page.evaluate(() => document.activeElement.closest('.target-row')?.dataset.targetIndex),
    '1',
    'moving a target keeps keyboard focus with it',
  );
  await editor.locator(`[data-target-index="${count}"] [data-target-action="delete"]`).click();
  assert.equal(await page.evaluate(() => document.activeElement !== document.body), true);

  await page.locator('#discardRoutesButton').click();
  await page.locator('#confirmAccept').click();
  await page.waitForSelector('#confirmDialog[open]', { state: 'hidden' });
  await page.waitForFunction(() => document.querySelector('#routeChangeBar').hidden);
  assert.equal(await alias.inputValue(), originalAlias);
  assert.equal(await strategy.inputValue(), originalStrategy);

  let releaseSave;
  let firstRequest;
  let writes = 0;
  const held = new Promise((resolve) => {
    releaseSave = resolve;
  });
  const requested = new Promise((resolve) => {
    firstRequest = resolve;
  });
  const savePattern = '**/admin/api/routes';
  await page.route(savePattern, async (route) => {
    if (route.request().method() === 'PUT') {
      writes++;
      firstRequest();
      await held;
    }
    await route.continue();
  });
  try {
    await strategy.selectOption(firstStrategy);
    await page.locator('#saveRoutesButton').click();
    await requested;
    const secondStrategy = firstStrategy === 'sticky' ? 'least_loaded' : 'sticky';
    await strategy.selectOption(secondStrategy);
    assert.equal(
      await page.locator('#saveRoutesButton').isDisabled(),
      true,
      'further edits cannot enable a duplicate save while a request is pending',
    );
    releaseSave();
    await page.waitForFunction(
      () => document.querySelector('#saveRoutesButton').getAttribute('aria-busy') === 'false',
    );
    assert.equal(writes, 1);
    assert.equal(await strategy.inputValue(), secondStrategy, 'a completed save cannot erase newer edits');
    assert.equal(await page.locator('#saveRoutesButton').isDisabled(), false);
    const server = await (await page.request.get(process.env.ADMIN_URL + '/api/state')).json();
    assert.equal(
      server.config.routes[originalAlias].strategy,
      firstStrategy,
      'only the submitted snapshot becomes live',
    );
  } finally {
    releaseSave();
    await page.unroute(savePattern);
  }
  await page.locator('#discardRoutesButton').click();
  await page.locator('#confirmAccept').click();
  await page.waitForSelector('#confirmDialog[open]', { state: 'hidden' });
  await page.waitForFunction(() => document.querySelector('#routeChangeBar').hidden);
  await strategy.selectOption(originalStrategy);
  await page.locator('#saveRoutesButton').click();
  await page.waitForFunction(() => document.querySelector('#routeChangeBar').hidden);
};
