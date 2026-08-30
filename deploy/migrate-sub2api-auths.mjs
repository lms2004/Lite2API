#!/usr/bin/env node
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';

process.umask(0o077);

const valueOptions = new Set(['input', 'cliproxy-auth-dir', 'grok-output', 'lite-output', 'owner']);
const booleanOptions = new Set(['allow-unsupported']);

function parseArgs(argv) {
  const result = {};
  for (let index = 0; index < argv.length; index++) {
    const option = argv[index];
    if (!option.startsWith('--')) throw new Error(`unexpected positional argument: ${option}`);
    const name = option.slice(2);
    if (booleanOptions.has(name)) {
      if (Object.hasOwn(result, name)) throw new Error(`duplicate --${name}`);
      result[name] = true;
      continue;
    }
    if (!valueOptions.has(name)) throw new Error(`unknown option: --${name}`);
    if (Object.hasOwn(result, name)) throw new Error(`duplicate --${name}`);
    const value = argv[++index];
    if (!value || value.startsWith('--')) throw new Error(`missing value for --${name}`);
    result[name] = value;
  }
  for (const name of ['input', 'cliproxy-auth-dir', 'grok-output', 'lite-output']) {
    if (!result[name]) throw new Error(`missing --${name}`);
  }
  return result;
}

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

const safe = value => String(value || 'account')
  .toLowerCase()
  .replace(/[^a-z0-9@._-]+/g, '-')
  .replace(/^-|-$/g, '')
  .slice(0, 80) || 'account';

const boundedNumber = (value, fallback, minimum, maximum) => {
  const number = Number(value);
  return Number.isFinite(number) ? Math.min(maximum, Math.max(minimum, number)) : fallback;
};

function parseOwner(value) {
  if (!value) return null;
  const match = /^(\d+):(\d+)$/.exec(value);
  if (!match) throw new Error('--owner must be numeric UID:GID');
  const uid = Number(match[1]);
  const gid = Number(match[2]);
  if (!Number.isSafeInteger(uid) || !Number.isSafeInteger(gid) || uid > 0x7fffffff || gid > 0x7fffffff) {
    throw new Error('--owner UID/GID is out of range');
  }
  return { uid, gid };
}

function assertRegularInput(input) {
  const stat = fs.lstatSync(input);
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error('input must be a regular, non-symlink file');
  if (stat.size > 128 * 1024 * 1024) throw new Error('input exceeds the 128 MiB safety limit');
}

function ensureDirectoryWithoutSymlinks(directory, mode = undefined) {
  const absolute = path.resolve(directory);
  const parsed = path.parse(absolute);
  let current = parsed.root;
  for (const component of absolute.slice(parsed.root.length).split(path.sep).filter(Boolean)) {
    current = path.join(current, component);
    try {
      const stat = fs.lstatSync(current);
      if (stat.isSymbolicLink() || !stat.isDirectory()) throw new Error(`unsafe output directory component: ${current}`);
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
      fs.mkdirSync(current, { mode: mode ?? 0o700 });
    }
  }
  if (mode !== undefined) fs.chmodSync(absolute, mode);
  return absolute;
}

function accountSuffix(account, credentials, platform, email, index) {
  const sourceID = get(account, 'id', 'uuid') || get(credentials, 'account_id', 'chatgpt_account_id', 'account_uuid', 'sub', 'project_id') || index + 1;
  return crypto.createHash('sha256')
    .update(JSON.stringify([platform, String(sourceID), String(email), index]))
    .digest('hex')
    .slice(0, 12);
}

function migrate(args) {
  let owner = parseOwner(args.owner);
  const ownerWasExplicit = owner !== null;
  const input = path.resolve(args.input);
  const authDir = path.resolve(args['cliproxy-auth-dir']);
  const grokOutput = path.resolve(args['grok-output']);
  const liteOutput = path.resolve(args['lite-output']);
  const outputs = [grokOutput, liteOutput];

  if (new Set([input, ...outputs]).size !== 3) throw new Error('input and aggregate outputs must be distinct paths');
  for (const output of outputs) {
    const relative = path.relative(authDir, output);
    if (relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..')) {
      throw new Error('aggregate outputs must be outside --cliproxy-auth-dir');
    }
  }

  assertRegularInput(input);
  const parsed = JSON.parse(fs.readFileSync(input, 'utf8'));
  const data = parsed?.data?.accounts ? parsed.data : parsed;
  if (!data || !Array.isArray(data.accounts)) throw new Error('input is not a Sub2API account export');

  const observedAt = new Date().toISOString();
  const counts = { lite: 0, gemini: 0, claude: 0, antigravity: 0, codex: 0, grok: 0, unsupported: 0 };
  const liteAccounts = [];
  const grokAccounts = [];
  const authArtifacts = [];
  const targetNames = new Set();

  for (const [index, account] of data.accounts.entries()) {
    if (!account || typeof account !== 'object' || Array.isArray(account)) throw new Error(`account ${index + 1} is not an object`);
    const credentials = account.credentials || {};
    if (!credentials || typeof credentials !== 'object' || Array.isArray(credentials)) throw new Error(`account ${index + 1} credentials are invalid`);
    const platform = String(account.platform || '').toLowerCase();
    const type = String(account.type || '').toLowerCase();
    const label = get(account, 'name') || get(credentials, 'email', 'email_address') || `${platform || 'account'}-${index + 1}`;
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
      const modelMap = credentials.model_mapping && typeof credentials.model_mapping === 'object' && !Array.isArray(credentials.model_mapping)
        ? credentials.model_mapping : {};
      liteAccounts.push({
        id: `sub2-${safe(platform)}-${accountSuffix(account, credentials, platform, email, index)}`,
        name: label,
        platform,
        type: 'api_key',
        base_url: baseURL,
        api_key: get(credentials, 'api_key'),
        models: Object.keys(modelMap),
        model_map: modelMap,
        priority: boundedNumber(account.priority, 0, -1000000, 1000000),
        weight: boundedNumber(account.weight, 1, 0, 1000000),
        concurrency: boundedNumber(account.concurrency || account.max_concurrent, 2, 1, 100000),
        enabled: account.enabled !== false
      });
      counts.lite++;
      continue;
    }

    if (!accessToken && !refreshToken) {
      counts.unsupported++;
      continue;
    }

    const suffix = accountSuffix(account, credentials, platform, email, index);
    const addAuth = (prefix, identity, value) => {
      const filename = `${prefix}-${safe(identity)}-${suffix}.json`;
      if (targetNames.has(filename)) throw new Error(`credential filename collision: ${filename}`);
      targetNames.add(filename);
      authArtifacts.push({ target: path.join(authDir, filename), value });
    };

    if (platform === 'gemini') {
      const projectID = get(credentials, 'project_id');
      addAuth('gemini', `${email || label}-${projectID || 'default'}`, {
        type: 'gemini', email, project_id: projectID, auto: false, checked: false,
        token: { access_token: accessToken, refresh_token: refreshToken, token_type: get(credentials, 'token_type') || 'Bearer', expiry: expired }
      });
      counts.gemini++;
    } else if (platform === 'anthropic' || platform === 'claude') {
      addAuth('claude', email || label, {
        type: 'claude', access_token: accessToken, refresh_token: refreshToken, email,
        account_uuid: get(credentials, 'account_uuid'), organization_uuid: get(credentials, 'org_uuid', 'organization_uuid'),
        last_refresh: observedAt, expired
      });
      counts.claude++;
    } else if (platform === 'antigravity') {
      addAuth('antigravity', email || label, {
        type: 'antigravity', access_token: accessToken, refresh_token: refreshToken, email,
        project_id: get(credentials, 'project_id'), token_type: get(credentials, 'token_type') || 'Bearer', expired
      });
      counts.antigravity++;
    } else if (platform === 'openai' || platform === 'codex') {
      addAuth('codex', email || label, {
        type: 'codex', access_token: accessToken, refresh_token: refreshToken, id_token: get(credentials, 'id_token'), email,
        account_id: get(credentials, 'chatgpt_account_id', 'account_id'), last_refresh: observedAt, expired
      });
      counts.codex++;
    } else if (platform === 'grok' || platform === 'xai') {
      grokAccounts.push({
        provider: 'grok_build', name: label, email, client_id: get(credentials, 'client_id'),
        access_token: accessToken, refresh_token: refreshToken, id_token: get(credentials, 'id_token'),
        sub: get(credentials, 'sub'), user_id: get(credentials, 'sub'), team_id: get(credentials, 'team_id'),
        token_type: get(credentials, 'token_type') || 'Bearer', expires_at: expired
      });
      counts.grok++;
    } else {
      counts.unsupported++;
    }
  }

  if (counts.unsupported && !args['allow-unsupported']) {
    const error = new Error(`refusing partial migration: ${counts.unsupported} unsupported account(s); inspect the export or pass --allow-unsupported`);
    error.counts = counts;
    throw error;
  }

  const artifacts = [
    ...authArtifacts,
    { target: grokOutput, value: { accounts: grokAccounts } },
    { target: liteOutput, value: { type: 'sub2api-data', version: 1, exported_at: observedAt, proxies: [], accounts: liteAccounts } }
  ];
  const absoluteTargets = artifacts.map(artifact => path.resolve(artifact.target));
  if (new Set(absoluteTargets).size !== absoluteTargets.length) throw new Error('two migration artifacts resolve to the same path');
  for (const target of absoluteTargets) {
    if (fs.existsSync(target)) throw new Error(`refusing to overwrite existing output: ${target}`);
  }

  ensureDirectoryWithoutSymlinks(authDir, 0o700);
  for (const output of outputs) ensureDirectoryWithoutSymlinks(path.dirname(output));
  if (!owner) {
    const directoryOwner = fs.statSync(authDir);
    owner = { uid: directoryOwner.uid, gid: directoryOwner.gid };
  }
  if (typeof process.geteuid === 'function' && process.geteuid() !== 0 && owner.uid !== process.geteuid()) {
    throw new Error(`credential directory belongs to UID ${owner.uid}; rerun as root or choose a directory owned by the current user`);
  }
  if (ownerWasExplicit) {
    fs.chownSync(authDir, owner.uid, owner.gid);
  }

  const stageRoots = new Map();
  const staged = [];
  const linked = [];
  try {
    for (const [index, artifact] of artifacts.entries()) {
      const target = path.resolve(artifact.target);
      const parent = path.dirname(target);
      let stageRoot = stageRoots.get(parent);
      if (!stageRoot) {
        stageRoot = fs.mkdtempSync(path.join(parent, '.lite2api-migration-'));
        fs.chmodSync(stageRoot, 0o700);
        stageRoots.set(parent, stageRoot);
      }
      const source = path.join(stageRoot, `${index}.json`);
      fs.writeFileSync(source, `${JSON.stringify(artifact.value, null, 2)}\n`, { mode: 0o600, flag: 'wx' });
      fs.chmodSync(source, 0o600);
      if (typeof process.geteuid !== 'function' || process.geteuid() === 0) fs.chownSync(source, owner.uid, owner.gid);
      staged.push({ source, target });
    }
    for (const artifact of staged) {
      // link(2) provides create-if-absent semantics; unlike rename it can never
      // silently replace a credential created by another migration.
      fs.linkSync(artifact.source, artifact.target);
      linked.push(artifact.target);
    }
  } catch (error) {
    for (const target of linked.reverse()) {
      try { fs.unlinkSync(target); } catch { /* best-effort transaction rollback */ }
    }
    throw error;
  } finally {
    for (const stageRoot of stageRoots.values()) fs.rmSync(stageRoot, { recursive: true, force: true });
  }

  return { ok: counts.unsupported === 0, counts, files_written: artifacts.length, owner: `${owner.uid}:${owner.gid}` };
}

try {
  const result = migrate(parseArgs(process.argv.slice(2)));
  console.log(JSON.stringify(result));
  if (!result.ok) process.exitCode = 2;
} catch (error) {
  console.error(JSON.stringify({ ok: false, error: error.message, ...(error.counts ? { counts: error.counts } : {}) }));
  process.exitCode = 1;
}
