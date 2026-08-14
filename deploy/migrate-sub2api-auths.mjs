#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';

const args = Object.fromEntries(process.argv.slice(2).map((value, index, all) => value.startsWith('--') ? [value.slice(2), all[index + 1]] : null).filter(Boolean));
for (const name of ['input', 'cliproxy-auth-dir', 'grok-output', 'lite-output']) {
  if (!args[name]) throw new Error(`missing --${name}`);
}

const parsed = JSON.parse(fs.readFileSync(args.input, 'utf8'));
const data = parsed?.data?.accounts ? parsed.data : parsed;
if (!data || !Array.isArray(data.accounts)) throw new Error('input is not a Sub2API account export');
fs.mkdirSync(args['cliproxy-auth-dir'], { recursive: true, mode: 0o700 });

const get = (object, ...names) => {
  for (const name of names) {
    const value = object?.[name];
    if (value !== undefined && value !== null && String(value).trim() !== '') return value;
  }
  return '';
};
const expiry = value => {
  if (value === '' || value === undefined || value === null) return '';
  const number = Number(value);
  if (Number.isFinite(number)) return new Date(number > 1e12 ? number : number * 1000).toISOString();
  const date = new Date(String(value));
  return Number.isNaN(date.valueOf()) ? '' : date.toISOString();
};
const safe = value => String(value || 'account').toLowerCase().replace(/[^a-z0-9@._-]+/g, '-').replace(/^-|-$/g, '').slice(0, 100) || 'account';
const writeSecretJSON = (target, value) => {
  fs.writeFileSync(target, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
  fs.chmodSync(target, 0o600);
};

const counts = { lite: 0, gemini: 0, claude: 0, antigravity: 0, codex: 0, grok: 0, unsupported: 0 };
const liteAccounts = [];
const grokAccounts = [];
for (const [index, account] of data.accounts.entries()) {
  const credentials = account.credentials || {};
  const platform = String(account.platform || '').toLowerCase();
  const type = String(account.type || '').toLowerCase();
  const label = get(account, 'name') || get(credentials, 'email', 'email_address') || `${platform}-${index + 1}`;
  const email = get(credentials, 'email', 'email_address') || (String(label).includes('@') ? label : '');
  const expired = expiry(get(credentials, 'expires_at', 'expired'));
  const accessToken = get(credentials, 'access_token');
  const refreshToken = get(credentials, 'refresh_token');
  const configuredBaseURL = get(credentials, 'base_url') || get(account.extra, 'base_url');
  const normalizedBaseURL = String(configuredBaseURL).replace(/\/+$/, '');
  const baseURL = platform === 'gemini' && (!normalizedBaseURL || normalizedBaseURL === 'https://generativelanguage.googleapis.com')
    ? 'https://generativelanguage.googleapis.com/v1beta/openai'
    : configuredBaseURL;

  if (['apikey', 'api_key'].includes(type) && get(credentials, 'api_key')) {
    const modelMap = credentials.model_mapping && typeof credentials.model_mapping === 'object' && !Array.isArray(credentials.model_mapping) ? credentials.model_mapping : {};
    liteAccounts.push({
      id: `sub2-${safe(platform)}-${index + 1}`,
      name: label,
      platform,
      type: 'api_key',
      base_url: baseURL,
      api_key: get(credentials, 'api_key'),
      models: Object.keys(modelMap),
      model_map: modelMap,
      priority: Number(account.priority || 0),
      weight: Number(account.weight || 1),
      concurrency: Number(account.concurrency || account.max_concurrent || 2),
      enabled: account.enabled !== false
    });
    counts.lite++;
    continue;
  }

  if (!accessToken && !refreshToken) {
    counts.unsupported++;
    continue;
  }

  if (platform === 'gemini') {
    const projectID = get(credentials, 'project_id');
    const auth = {
      type: 'gemini', email, project_id: projectID, auto: false, checked: false,
      token: { access_token: accessToken, refresh_token: refreshToken, token_type: get(credentials, 'token_type') || 'Bearer', expiry: expired }
    };
    writeSecretJSON(path.join(args['cliproxy-auth-dir'], `gemini-${safe(email || label)}-${safe(projectID || index + 1)}.json`), auth);
    counts.gemini++;
    continue;
  }
  if (platform === 'anthropic' || platform === 'claude') {
    writeSecretJSON(path.join(args['cliproxy-auth-dir'], `claude-${safe(email || label)}.json`), {
      type: 'claude', access_token: accessToken, refresh_token: refreshToken, email,
      account_uuid: get(credentials, 'account_uuid'), organization_uuid: get(credentials, 'org_uuid', 'organization_uuid'),
      last_refresh: new Date().toISOString(), expired
    });
    counts.claude++;
    continue;
  }
  if (platform === 'antigravity') {
    writeSecretJSON(path.join(args['cliproxy-auth-dir'], `antigravity-${safe(email || label)}.json`), {
      type: 'antigravity', access_token: accessToken, refresh_token: refreshToken, email,
      project_id: get(credentials, 'project_id'), token_type: get(credentials, 'token_type') || 'Bearer', expired
    });
    counts.antigravity++;
    continue;
  }
  if (platform === 'openai' || platform === 'codex') {
    writeSecretJSON(path.join(args['cliproxy-auth-dir'], `codex-${safe(email || label)}.json`), {
      type: 'codex', access_token: accessToken, refresh_token: refreshToken, id_token: get(credentials, 'id_token'), email,
      account_id: get(credentials, 'chatgpt_account_id', 'account_id'), last_refresh: new Date().toISOString(), expired
    });
    counts.codex++;
    continue;
  }
  if (platform === 'grok' || platform === 'xai') {
    grokAccounts.push({
      provider: 'grok_build', name: label, email, client_id: get(credentials, 'client_id'),
      access_token: accessToken, refresh_token: refreshToken, id_token: get(credentials, 'id_token'),
      sub: get(credentials, 'sub'), user_id: get(credentials, 'sub'), team_id: get(credentials, 'team_id'),
      token_type: get(credentials, 'token_type') || 'Bearer', expires_at: expired
    });
    counts.grok++;
    continue;
  }
  counts.unsupported++;
}

writeSecretJSON(args['grok-output'], { accounts: grokAccounts });
writeSecretJSON(args['lite-output'], { type: 'sub2api-data', version: 1, exported_at: new Date().toISOString(), proxies: [], accounts: liteAccounts });
console.log(JSON.stringify({ ok: counts.unsupported === 0, counts }));
if (counts.unsupported) process.exitCode = 2;
