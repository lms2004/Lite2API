import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const script = path.join(path.dirname(fileURLToPath(import.meta.url)), 'migrate-sub2api-auths.mjs');

function fixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'lite2api-migration-test-'));
  const input = path.join(root, 'input.json');
  const authDir = path.join(root, 'auths');
  fs.mkdirSync(authDir, { mode: 0o700 });
  const grokOutput = path.join(root, 'grok.json');
  const liteOutput = path.join(root, 'lite.json');
  const run = extra => spawnSync(process.execPath, [script,
    '--input', input,
    '--cliproxy-auth-dir', authDir,
    '--grok-output', grokOutput,
    '--lite-output', liteOutput,
    ...extra
  ], { encoding: 'utf8' });
  return { root, input, authDir, grokOutput, liteOutput, run };
}

test('same-email credentials receive stable collision-free names', t => {
  const files = fixture();
  t.after(() => fs.rmSync(files.root, { recursive: true, force: true }));
  fs.writeFileSync(files.input, JSON.stringify({ accounts: [
    { id: 'first', platform: 'openai', type: 'oauth', credentials: { email: 'same@example.com', access_token: 'a' } },
    { id: 'second', platform: 'openai', type: 'oauth', credentials: { email: 'same@example.com', access_token: 'b' } }
  ] }));

  const result = files.run([]);
  assert.equal(result.status, 0, result.stderr);
  const credentials = fs.readdirSync(files.authDir).filter(name => name.endsWith('.json'));
  assert.equal(credentials.length, 2);
  assert.notEqual(credentials[0], credentials[1]);
  for (const name of credentials) assert.equal(fs.statSync(path.join(files.authDir, name)).mode & 0o777, 0o600);
});

test('unsupported input fails before writing any artifact', t => {
  const files = fixture();
  t.after(() => fs.rmSync(files.root, { recursive: true, force: true }));
  fs.writeFileSync(files.input, JSON.stringify({ accounts: [
    { platform: 'unknown', type: 'oauth', credentials: { access_token: 'secret' } }
  ] }));

  const result = files.run([]);
  assert.equal(result.status, 1);
  assert.equal(fs.existsSync(files.grokOutput), false);
  assert.equal(fs.existsSync(files.liteOutput), false);
  assert.deepEqual(fs.readdirSync(files.authDir), []);
});

test('an existing aggregate output is never overwritten', t => {
  const files = fixture();
  t.after(() => fs.rmSync(files.root, { recursive: true, force: true }));
  fs.writeFileSync(files.input, JSON.stringify({ accounts: [
    { id: 'codex', platform: 'codex', type: 'oauth', credentials: { email: 'a@example.com', access_token: 'secret' } }
  ] }));
  fs.writeFileSync(files.liteOutput, 'keep-me');

  const result = files.run([]);
  assert.equal(result.status, 1);
  assert.equal(fs.readFileSync(files.liteOutput, 'utf8'), 'keep-me');
  assert.equal(fs.existsSync(files.grokOutput), false);
  assert.deepEqual(fs.readdirSync(files.authDir), []);
});
