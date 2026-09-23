#!/usr/bin/env node
// 慧沐引擎 · Agent 一键接入助手
// 零依赖（Node >= 18）。支持的编码工具与配置文件位置对齐智谱 coding-helper：
//   claude-code   ~/.claude/settings.json（env 注入，走本站 /v1/messages Anthropic 端点）
//   codex         ~/.codex/config.toml（自定义 provider，wire_api=responses，走 /v1/responses）
//   opencode      ~/.config/opencode/opencode.json（openai-compatible provider）
//   crush         ~/.config/crush/crush.json（providers.huimu）
//   factory-droid ~/.factory/settings.json（customModels，generic-chat-completion-api）
// 用法：
//   node helper.mjs                                   # 交互式向导
//   node helper.mjs install claude-code codex         # 安装指定 agent
//   node helper.mjs uninstall claude-code             # 卸载指定 agent 配置
//   node helper.mjs status                            # 查看各 agent 配置状态
//   node helper.mjs selftest                          # 内部纯函数自检
// flags: --base http://host:port   --key sk-...   --model <模型名>   --yes
import { existsSync, mkdirSync, readFileSync, writeFileSync, renameSync, mkdtempSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { homedir, tmpdir } from 'node:os';
import { createInterface } from 'node:readline/promises';
import { stdin, stdout } from 'node:process';

const DEFAULT_BASE = '__HUIMU_BASE__'; // 网关下发时注入实际地址
const PROVIDER = 'huimu';
const VERSION = '1.0.0';

// ---------------- 参数解析 ----------------
// flag 可出现在任意位置（引导器会把 --base 前置），第一个位置参数是命令，其余是命令参数
function parseArgs(argv) {
  const flags = { _: [] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === '--base') flags.base = argv[++i];
    else if (a === '--key') flags.key = argv[++i];
    else if (a === '--model') flags.model = argv[++i];
    else if (a === '--yes') flags.yes = true;
    else if (a.startsWith('-')) { /* 忽略未知 flag */ }
    else flags._.push(a);
  }
  const command = flags._.length > 0 ? flags._.shift() : null;
  return { command, flags };
}

// ---------------- 通用小工具 ----------------
function readJSON(path) {
  try {
    if (existsSync(path)) return JSON.parse(readFileSync(path, 'utf-8'));
  } catch { /* 损坏文件按空配置处理，安装路径会覆盖 */ }
  return {};
}

function writeAtomic(path, content) {
  mkdirSync(dirname(path), { recursive: true });
  const tmp = path + '.huimu-tmp';
  writeFileSync(tmp, content, 'utf-8');
  renameSync(tmp, path);
}

function writeJSON(path, obj) {
  writeAtomic(path, JSON.stringify(obj, null, 2) + '\n');
}

async function ask(rl, question) {
  const answer = (await rl.question(question)).trim();
  return answer;
}

function log(msg) { console.log(msg); }
function warn(msg) { console.log('\x1b[33m' + msg + '\x1b[0m'); }
function ok(msg) { console.log('\x1b[32m' + msg + '\x1b[0m'); }
function die(msg) { console.error('\x1b[31m' + msg + '\x1b[0m'); process.exit(1); }

// ---------------- Codex config.toml 行级编辑 ----------------
// TOML 无法零依赖安全重序列化（会丢注释/格式），采用确定性行级编辑：
// 顶层键只动第一个表头之前的区域；我们自己的 [model_providers.huimu] 段整段增删。

function removeTomlTable(text, header) {
  const lines = text.split('\n');
  const out = [];
  let skipping = false;
  const headRe = new RegExp('^\\s*\\[' + header.replace(/\./g, '\\.') + '\\]\\s*$');
  for (const line of lines) {
    if (skipping) {
      if (/^\s*\[/.test(line)) { skipping = false; out.push(line); }
      continue;
    }
    if (headRe.test(line)) { skipping = true; continue; }
    out.push(line);
  }
  // 尾部空段清理
  while (out.length > 0 && out[out.length - 1] === '') out.pop();
  return out.join('\n');
}

function removeTopLevelTomlKeys(text, keys) {
  const lines = text.split('\n');
  const out = [];
  let inTable = false;
  const keyRe = new RegExp('^\\s*(' + keys.join('|') + ')\\s*=');
  for (const line of lines) {
    if (/^\s*\[/.test(line)) inTable = true;
    if (!inTable && keyRe.test(line)) continue;
    out.push(line);
  }
  return out.join('\n');
}

function installCodexToml(text, { base, key, model }) {
  let t = removeTomlTable(text, `model_providers.${PROVIDER}`);
  t = removeTopLevelTomlKeys(t, ['model_provider', 'model', 'model_reasoning_effort', 'model_catalog_json']);
  const head = `model_provider = "${PROVIDER}"\nmodel = "${model}"\n`;
  const table = [
    '',
    `[model_providers.${PROVIDER}]`,
    'name = "huimu"',
    `base_url = "${base}/v1"`,
    `experimental_bearer_token = "${key}"`,
    'wire_api = "responses"', // codex 0.142+ 已移除 chat，自定义 provider 只认 responses
  ].join('\n');
  t = head + t.replace(/^\n+/, '');
  if (t.trim() !== '') t = t.trimEnd() + '\n';
  return t + table + '\n';
}

function uninstallCodexToml(text) {
  // 存在我们的 provider 表 = 安装过，顶层 model/model_provider 均为我们写入，一并回收；
  // 没装过（用户自配其它 provider）则一个键都不动
  const ours = text.includes(`[model_providers.${PROVIDER}]`);
  let t = removeTomlTable(text, `model_providers.${PROVIDER}`);
  const lines = t.split('\n');
  const out = [];
  let inTable = false;
  for (const line of lines) {
    if (/^\s*\[/.test(line)) inTable = true;
    if (!inTable && ours && (
      /^\s*(model_provider|model|model_reasoning_effort|model_catalog_json)\s*=/.test(line)
    )) continue;
    out.push(line);
  }
  return out.join('\n');
}

// ---------------- Agent 定义 ----------------
// 所有写入均「外科手术式」：只增删自己的键，保留用户已有配置；卸载可完整还原。

function agentDefs(ctx) {
  const home = ctx.home;
  const { base, key, model } = ctx;
  const chatBase = `${base}/v1`;
  return {
    'claude-code': {
      name: 'Claude Code',
      configPath: join(home, '.claude', 'settings.json'),
      install() {
        const p = join(home, '.claude', 'settings.json');
        const cfg = readJSON(p);
        const env = { ...(cfg.env || {}) };
        Object.assign(env, {
          ANTHROPIC_AUTH_TOKEN: key,
          ANTHROPIC_BASE_URL: base, // Claude Code 自行拼接 /v1/messages
          API_TIMEOUT_MS: '3000000',
          CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
          // 模型映射：把 sonnet/opus/haiku 槽位都指到本站模型
          ANTHROPIC_DEFAULT_HAIKU_MODEL: model,
          ANTHROPIC_DEFAULT_SONNET_MODEL: model,
          ANTHROPIC_DEFAULT_OPUS_MODEL: model,
        });
        writeJSON(p, { ...cfg, env });
        // 跳过 Claude Code 首次启动向导（与智谱 helper 同法）
        const ob = join(home, '.claude.json');
        const obCfg = readJSON(ob);
        if (!obCfg.hasCompletedOnboarding) writeJSON(ob, { ...obCfg, hasCompletedOnboarding: true });
      },
      uninstall() {
        const p = join(home, '.claude', 'settings.json');
        if (!existsSync(p)) return;
        const cfg = readJSON(p);
        if (!cfg.env) return;
        const env = { ...cfg.env };
        for (const k of [
          'ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_BASE_URL', 'API_TIMEOUT_MS',
          'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC', 'CLAUDE_CODE_AUTO_COMPACT_WINDOW',
          'ANTHROPIC_DEFAULT_HAIKU_MODEL', 'ANTHROPIC_DEFAULT_SONNET_MODEL', 'ANTHROPIC_DEFAULT_OPUS_MODEL',
          'ANTHROPIC_API_KEY',
        ]) delete env[k];
        const next = { ...cfg, env };
        if (Object.keys(env).length === 0) delete next.env;
        writeJSON(p, next);
      },
      status() {
        const env = readJSON(join(home, '.claude', 'settings.json')).env || {};
        return env.ANTHROPIC_BASE_URL === base ? `已接入（模型 ${env.ANTHROPIC_DEFAULT_SONNET_MODEL || '?'}）`
          : env.ANTHROPIC_BASE_URL ? `已接入其它服务（${env.ANTHROPIC_BASE_URL}）` : '未配置';
      },
    },
    'codex': {
      name: 'Codex CLI',
      configPath: join(home, '.codex', 'config.toml'),
      install() {
        const p = join(home, '.codex', 'config.toml');
        const text = existsSync(p) ? readFileSync(p, 'utf-8') : '';
        writeAtomic(p, installCodexToml(text, { base, key, model }));
      },
      uninstall() {
        const p = join(home, '.codex', 'config.toml');
        if (!existsSync(p)) return;
        writeAtomic(p, uninstallCodexToml(readFileSync(p, 'utf-8')));
      },
      status() {
        const p = join(home, '.codex', 'config.toml');
        if (!existsSync(p)) return '未配置';
        const t = readFileSync(p, 'utf-8');
        return t.includes(`[model_providers.${PROVIDER}]`) ? '已接入' : '未配置';
      },
    },
    'opencode': {
      name: 'OpenCode',
      configPath: join(home, '.config', 'opencode', 'opencode.json'),
      install() {
        const p = join(home, '.config', 'opencode', 'opencode.json');
        const cfg = readJSON(p);
        const provider = { ...(cfg.provider || {}) };
        provider[PROVIDER] = {
          npm: '@ai-sdk/openai-compatible',
          name: 'huimu',
          options: { baseURL: chatBase, apiKey: key },
          models: [{ name: model }],
        };
        writeJSON(p, {
          $schema: 'https://opencode.ai/config.json',
          ...cfg,
          provider,
          model: `${PROVIDER}/${model}`,
          small_model: `${PROVIDER}/${model}`,
        });
      },
      uninstall() {
        const p = join(home, '.config', 'opencode', 'opencode.json');
        if (!existsSync(p)) return;
        const cfg = readJSON(p);
        if (cfg.provider) {
          delete cfg.provider[PROVIDER];
          if (Object.keys(cfg.provider).length === 0) delete cfg.provider;
        }
        if (typeof cfg.model === 'string' && cfg.model.startsWith(PROVIDER + '/')) delete cfg.model;
        if (typeof cfg.small_model === 'string' && cfg.small_model.startsWith(PROVIDER + '/')) delete cfg.small_model;
        writeJSON(p, cfg);
      },
      status() {
        const cfg = readJSON(join(home, '.config', 'opencode', 'opencode.json'));
        if (cfg.provider && cfg.provider[PROVIDER]) return `已接入（模型 ${cfg.model || '?'}）`;
        return cfg.provider && Object.keys(cfg.provider).length ? '已接入其它服务' : '未配置';
      },
    },
    'crush': {
      name: 'Crush',
      configPath: join(home, '.config', 'crush', 'crush.json'),
      install() {
        const p = join(home, '.config', 'crush', 'crush.json');
        const cfg = readJSON(p);
        writeJSON(p, {
          ...cfg,
          providers: {
            ...(cfg.providers || {}),
            [PROVIDER]: { id: PROVIDER, name: 'Huimu', base_url: chatBase, api_key: key },
          },
        });
      },
      uninstall() {
        const p = join(home, '.config', 'crush', 'crush.json');
        if (!existsSync(p)) return;
        const cfg = readJSON(p);
        if (cfg.providers) {
          delete cfg.providers[PROVIDER];
          if (Object.keys(cfg.providers).length === 0) delete cfg.providers;
        }
        writeJSON(p, cfg);
      },
      status() {
        const cfg = readJSON(join(home, '.config', 'crush', 'crush.json'));
        return cfg.providers && cfg.providers[PROVIDER] ? '已接入' : '未配置';
      },
    },
    'factory-droid': {
      name: 'Factory Droid',
      configPath: join(home, '.factory', 'settings.json'),
      install() {
        const p = join(home, '.factory', 'settings.json');
        const cfg = readJSON(p);
        const kept = (cfg.customModels || []).filter((m) => !String(m.displayName || '').includes('Huimu'));
        writeJSON(p, {
          ...cfg,
          customModels: [...kept, {
            displayName: `Huimu Engine [${model}] - Openai`,
            model,
            baseUrl: chatBase,
            apiKey: key,
            provider: 'generic-chat-completion-api',
            maxOutputTokens: 131072,
          }],
        });
      },
      uninstall() {
        const p = join(home, '.factory', 'settings.json');
        if (!existsSync(p)) return;
        const cfg = readJSON(p);
        if (!cfg.customModels) return;
        const kept = cfg.customModels.filter((m) => !String(m.displayName || '').includes('Huimu'));
        const next = { ...cfg, customModels: kept };
        if (kept.length === 0) delete next.customModels;
        writeJSON(p, next);
      },
      status() {
        const cfg = readJSON(join(home, '.factory', 'settings.json'));
        const hit = (cfg.customModels || []).find((m) => String(m.displayName || '').includes('Huimu'));
        return hit ? `已接入（模型 ${hit.model}）` : '未配置';
      },
    },
  };
}

const AGENT_IDS = ['claude-code', 'codex', 'opencode', 'crush', 'factory-droid'];

// ---------------- 密钥校验与模型拉取 ----------------
async function fetchModels(base, key) {
  const res = await fetch(`${base}/v1/models`, {
    headers: { Authorization: `Bearer ${key}` },
    signal: AbortSignal.timeout(10000),
  });
  if (!res.ok) {
    const body = await res.text().catch(() => '');
    throw new Error(`密钥校验失败（HTTP ${res.status}）${body.slice(0, 200)}`);
  }
  const data = await res.json();
  return (data.data || []).map((m) => m.id).filter(Boolean);
}

// ---------------- 命令实现 ----------------
function resolveBase(flags) {
  const base = (flags.base || DEFAULT_BASE).replace(/\/+$/, '');
  if (!/^https?:\/\//.test(base)) { // 占位符未被替换 = 不是从网关入口运行的
    die('缺少网关地址：请用 --base http://<host>:<port> 指定（或从网关 /agent-helper 入口运行，地址自动注入）');
  }
  return base;
}

async function resolveModel(flags, base, key, rl) {
  if (flags.model) return flags.model;
  let models = [];
  try {
    models = await fetchModels(base, key);
  } catch (e) {
    warn(`${e.message}，模型列表不可用`);
  }
  if (models.length === 0) {
    if (!rl) die('无法确定模型：请用 --model 指定（密钥无效或网关不可达）');
    return ask(rl, '模型名（如 deepseek-v4-flash）: ');
  }
  if (!rl) return models[0];
  log('\n可用模型：');
  models.forEach((m, i) => log(`  ${i + 1}. ${m}`));
  const pick = await ask(rl, `选择模型序号（回车默认 1）: `);
  const idx = parseInt(pick, 10);
  return idx >= 1 && idx <= models.length ? models[idx - 1] : models[0];
}

async function cmdInstall(flags, agents, base) {
  const key = flags.key || process.env.HUIMU_API_KEY;
  if (!key || !key.startsWith('sk-')) die('缺少有效密钥：请用 --key sk-... 指定（或设置 HUIMU_API_KEY 环境变量）');
  const rl = flags.yes ? null : await createInterface({ input: stdin, output: stdout });
  try {
    const model = await resolveModel(flags, base, key, rl);
    const ctx = { home: homedir(), base, key, model };
    const defs = agentDefs(ctx);
    log('');
    for (const id of agents) {
      const d = defs[id];
      try {
        d.install();
        ok(`✓ ${d.name} 已接入（模型 ${model}）→ ${d.configPath}`);
      } catch (e) {
        warn(`✗ ${d.name} 安装失败：${e.message}`);
      }
    }
    log(`\n完成。卸载任一 agent：node helper.mjs uninstall ${agents.join(' ')}`);
  } finally {
    if (rl) await rl.close();
  }
}

function cmdUninstall(agents) {
  const defs = agentDefs({ home: homedir(), base: '', key: '', model: '' });
  for (const id of agents) {
    const d = defs[id];
    try {
      d.uninstall();
      ok(`✓ ${d.name} 已卸载本站配置（其余配置保留）`);
    } catch (e) {
      warn(`✗ ${d.name} 卸载失败：${e.message}`);
    }
  }
}

function cmdStatus(base) {
  const defs = agentDefs({ home: homedir(), base, key: '', model: '' });
  log(`支持的编码工具（网关 ${base}）：`);
  for (const id of AGENT_IDS) {
    log(`  ${defs[id].name.padEnd(14)} ${defs[id].status()}`);
  }
}

async function wizard(flags, base) {
  log('慧沐引擎 · Agent 一键接入助手\n');
  const rl = await createInterface({ input: stdin, output: stdout });
  try {
    let key = flags.key || process.env.HUIMU_API_KEY || '';
    if (!key.startsWith('sk-')) {
      key = await ask(rl, 'API 密钥（sk- 开头，管理台「我的密钥」创建）: ');
    }
    let models = [];
    try {
      models = await fetchModels(base, key);
      ok(`密钥有效，可用模型 ${models.length} 个`);
    } catch (e) {
      warn(e.message);
    }
    log('\n要接入的编码工具（逗号分隔序号，回车全选）：');
    AGENT_IDS.forEach((id, i) => log(`  ${i + 1}. ${agentDefs({ home: '', base: '', key: '', model: '' })[id].name}`));
    const pick = await ask(rl, '选择: ');
    let ids = pick === '' ? AGENT_IDS : pick.split(/[,，\s]+/).map((n) => AGENT_IDS[parseInt(n, 10) - 1]).filter(Boolean);
    if (ids.length === 0) ids = AGENT_IDS;

    let model = flags.model || '';
    if (models.length > 0) {
      log('\n可用模型：');
      models.forEach((m, i) => log(`  ${i + 1}. ${m}`));
      const mpick = await ask(rl, `选择模型序号（回车默认 1）: `);
      const idx = parseInt(mpick, 10);
      model = idx >= 1 && idx <= models.length ? models[idx - 1] : models[0];
    } else {
      model = model || await ask(rl, '模型名: ');
    }

    const ctx = { home: homedir(), base, key, model };
    const defs = agentDefs(ctx);
    log('');
    for (const id of ids) {
      const d = defs[id];
      try {
        d.install();
        ok(`✓ ${d.name} 已接入（模型 ${model}）→ ${d.configPath}`);
      } catch (e) {
        warn(`✗ ${d.name} 安装失败：${e.message}`);
      }
    }
    log(`\n完成。卸载：node helper.mjs uninstall ${ids.join(' ')}`);
  } finally {
    await rl.close();
  }
}

// ---------------- 自检（CI/本地回归）----------------
function selftest() {
  const home = mkdtempSync(join(tmpdir(), 'huimu-helper-'));
  const ctx = { home, base: 'http://gw.test:8080', key: 'sk-test-0001', model: 'm-alpha' };
  const defs = agentDefs(ctx);
  let failed = 0;
  const expect = (name, cond, detail) => {
    if (cond) ok(`✓ ${name}`);
    else { failed++; warn(`✗ ${name} ${detail || ''}`); }
  };

  // claude-code 安装/卸载往返
  defs['claude-code'].install();
  const s1 = readJSON(join(home, '.claude', 'settings.json'));
  expect('claude env 注入', s1.env && s1.env.ANTHROPIC_BASE_URL === ctx.base && s1.env.ANTHROPIC_AUTH_TOKEN === ctx.key);
  expect('claude onboarding 标记', readJSON(join(home, '.claude.json')).hasCompletedOnboarding === true);
  defs['claude-code'].uninstall();
  const s2 = readJSON(join(home, '.claude', 'settings.json'));
  expect('claude 卸载后 env 清空', !s2.env || Object.keys(s2.env).length === 0);

  // codex toml：空文件安装、用户已有内容保留、幂等重装、卸载还原
  defs['codex'].install();
  const t1 = readFileSync(join(home, '.codex', 'config.toml'), 'utf-8');
  expect('codex 顶层键', t1.startsWith('model_provider = "huimu"\nmodel = "m-alpha"'));
  expect('codex provider 段', t1.includes('[model_providers.huimu]') && t1.includes('wire_api = "responses"'));
  writeFileSync(join(home, '.codex', 'config.toml'),
    '# 用户注释\nuser_key = 1\nmodel = "user-model"\n\n[other]\nx = 2\n');
  defs['codex'].install();
  const t2 = readFileSync(join(home, '.codex', 'config.toml'), 'utf-8');
  expect('codex 用户配置保留', t2.includes('# 用户注释') && t2.includes('user_key = 1') && t2.includes('[other]'));
  expect('codex 用户顶层键不被删', t2.includes('user_key = 1'));
  expect('codex 我们顶层键前置', t2.indexOf('model_provider = "huimu"') === 0);
  const t3 = installCodexToml(t2, ctx); // 幂等
  expect('codex 幂等（唯一 provider 段）', (t3.match(/\[model_providers\.huimu\]/g) || []).length === 1);
  defs['codex'].uninstall();
  const t4 = readFileSync(join(home, '.codex', 'config.toml'), 'utf-8');
  expect('codex 卸载还原', !t4.includes('huimu') && !t4.includes('"m-alpha"') && t4.includes('# 用户注释') && t4.includes('user_key = 1'));
  // 用户自配 model（model_provider 非 huimu）时卸载不得回收
  writeFileSync(join(home, '.codex', 'config.toml'), 'model = "keep-me"\n');
  defs['codex'].uninstall();
  expect('codex 用户 model 保留', readFileSync(join(home, '.codex', 'config.toml'), 'utf-8').includes('keep-me'));

  // opencode / crush / factory-droid 往返
  defs['opencode'].install();
  const o1 = readJSON(join(home, '.config', 'opencode', 'opencode.json'));
  expect('opencode provider', o1.provider.huimu.options.baseURL === 'http://gw.test:8080/v1' && o1.model === 'huimu/m-alpha');
  writeFileSync(join(home, '.config', 'opencode', 'opencode.json'), JSON.stringify({ provider: { keep: { options: {} } } }));
  defs['opencode'].install();
  const o2 = readJSON(join(home, '.config', 'opencode', 'opencode.json'));
  expect('opencode 他人 provider 保留', !!o2.provider.keep);
  defs['opencode'].uninstall();
  const o3 = readJSON(join(home, '.config', 'opencode', 'opencode.json'));
  expect('opencode 卸载还原', !!o3.provider.keep && !o3.provider.huimu && !o3.model);

  defs['crush'].install();
  const c1 = readJSON(join(home, '.config', 'crush', 'crush.json'));
  expect('crush provider', c1.providers.huimu.base_url === 'http://gw.test:8080/v1');
  defs['crush'].uninstall();
  expect('crush 卸载还原', !readJSON(join(home, '.config', 'crush', 'crush.json')).providers);

  defs['factory-droid'].install();
  const f1 = readJSON(join(home, '.factory', 'settings.json'));
  expect('droid customModels', f1.customModels.length === 1 && f1.customModels[0].provider === 'generic-chat-completion-api');
  writeFileSync(join(home, '.factory', 'settings.json'), JSON.stringify({ customModels: [{ displayName: 'My Own', model: 'x' }] }));
  defs['factory-droid'].install();
  const f2 = readJSON(join(home, '.factory', 'settings.json'));
  expect('droid 已有模型保留', f2.customModels.length === 2);
  defs['factory-droid'].uninstall();
  const f3 = readJSON(join(home, '.factory', 'settings.json'));
  expect('droid 卸载还原', f3.customModels.length === 1 && f3.customModels[0].displayName === 'My Own');

  if (failed > 0) die(`\n自检失败 ${failed} 项`);
  ok('\n自检全部通过');
}

// ---------------- 入口 ----------------
async function main() {
  const argv = process.argv.slice(2);
  const { command, flags } = parseArgs(argv);

  if (command === 'selftest') return selftest();

  const base = resolveBase(flags);
  if (command === 'status') return cmdStatus(base);

  if (command === 'install') {
    const agents = flags._.filter((a) => AGENT_IDS.includes(a));
    if (agents.length === 0) die(`请指定要接入的 agent：${AGENT_IDS.join(' / ')}（或多个空格分隔）`);
    const bad = flags._.filter((a) => !AGENT_IDS.includes(a));
    if (bad.length) warn(`忽略不支持的 agent：${bad.join(', ')}（支持：${AGENT_IDS.join(' / ')}）`);
    return cmdInstall(flags, agents, base);
  }
  if (command === 'uninstall') {
    if (flags._.includes('all')) return cmdUninstall(AGENT_IDS);
    const agents = flags._.filter((a) => AGENT_IDS.includes(a));
    if (agents.length === 0) die(`请指定要卸载的 agent：${AGENT_IDS.join(' / ')}，或 uninstall all`);
    return cmdUninstall(agents);
  }
  if (command && command !== 'wizard') die(`未知命令：${command}\n用法：helper.mjs [install|uninstall|status|selftest] [agent...] [--base --key --model --yes]`);
  return wizard(flags, base);
}

main().catch((e) => die(e.message));
