#!/usr/bin/env node
// 慧沐引擎 · Agent 一键接入助手
// 零依赖（Node >= 18）。支持的编码工具与配置文件位置对齐智谱 coding-helper：
//   claude-code   ~/.claude/settings.json（env 注入，走本站 /v1/messages Anthropic 端点）
//   codex         ~/.codex/config.toml（自定义 provider，wire_api=responses，走 /v1/responses）
//                 + ~/.codex/models.json（模型元数据，桌面端 ChatGPT 内置 Codex 识别用）
//   opencode      ~/.config/opencode/opencode.json（openai-compatible provider）
//   trae-agent    ~/.trae-agent/trae_config.yaml（字节开源 CLI，YAML 三段式；trae-cli 默认只认
//                 当前目录的 trae_config.yaml，故同时把 TRAE_CONFIG_FILE 写进 shell rc）
// 用法：
//   node helper.mjs                                   # 交互式向导（状态总览 + 方向键选择 接入/卸载）
//   node helper.mjs install claude-code codex         # 安装指定 agent
//   node helper.mjs uninstall claude-code             # 卸载指定 agent 配置
//   node helper.mjs status                            # 查看各 agent 配置状态
//   node helper.mjs selftest                          # 内部纯函数自检
// flags: --base http://host:port   --key sk-...   --model <模型名>   --yes
import { existsSync, mkdirSync, readFileSync, writeFileSync, renameSync, mkdtempSync, unlinkSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { homedir, tmpdir } from 'node:os';
import { createInterface } from 'node:readline/promises';
import { emitKeypressEvents, createInterface as createLineInterface } from 'node:readline';
import { stdin, stdout } from 'node:process';

const DEFAULT_BASE = '__HUIMU_BASE__'; // 网关下发时注入实际地址
const PROVIDER = 'huimu';
const VERSION = '1.4.0';

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

// readText 读文本文件；不存在/损坏返回空串（状态探测用，不抛错）
function readText(path) {
  try {
    if (existsSync(path)) return readFileSync(path, 'utf-8');
  } catch { /* ignore */ }
  return '';
}

// shellRcPath 按登录 shell 选 rc 文件：trae-cli 默认只认当前目录的 trae_config.yaml，
// 固定路径的配置要靠 TRAE_CONFIG_FILE 环境变量（run/interactive/show-config 都读它），
// 必须随 shell 启动生效才有意义
function shellRcPath(home) {
  const sh = (process.env.SHELL || '').split('/').pop() || '';
  if (sh.includes('bash')) return join(home, '.bashrc');
  if (sh.includes('zsh')) return join(home, '.zshrc');
  return join(home, '.profile'); // 其它 shell 兜底：POSIX export 语法各 shell 通用
}

// addShellRcExport 向 rc 追加带标记的 export 块（幂等：已有标记不重复追加）
function addShellRcExport(rcPath, name, value) {
  const open = `# >>> ${name} huimu >>>`;
  const block = `${open}\nexport ${name}="${value}"\n# <<< ${name} huimu <<<\n`;
  const cur = readText(rcPath);
  if (cur.includes(open)) return;
  writeAtomic(rcPath, cur ? cur.replace(/\n*$/, '\n') + block : block);
}

// removeShellRcExport 摘掉指定变量的标记块；摘完 rc 若已空则整体回收（本助手建的场景）
function removeShellRcExport(rcPath, name) {
  const cur = readText(rcPath);
  if (!cur) return;
  const re = new RegExp(`\\n*# >>> ${name} huimu >>>\\n[\\s\\S]*?# <<< ${name} huimu <<<\\n?`, 'g');
  const next = cur.replace(re, '');
  if (next === cur) return;
  if (next.trim() === '') { try { unlinkSync(rcPath); } catch { /* ignore */ } return; }
  writeAtomic(rcPath, next);
}

// 文本输入：TTY 下按次创建/关闭 readline（不与 keySelect 的 raw mode 冲突）；
// 管道输入下 readline 会一口气吐出所有 line 事件（问句间隙没有 pending question 时行会被丢弃），
// 因此自维护行队列，按需取行。
let pipeLines = null;
function pipeLineReader() {
  if (pipeLines) return pipeLines;
  const queue = [];
  const waiters = [];
  const rl = createLineInterface({ input: stdin }); // 同步事件式读取
  rl.on('line', (line) => {
    const w = waiters.shift();
    if (w) w.resolve(line); else queue.push(line);
  });
  rl.on('close', () => { while (waiters.length) waiters.shift().resolve(''); }); // EOF：等待者收到空串
  pipeLines = {
    next: () => new Promise((resolve) => (queue.length ? resolve(queue.shift()) : waiters.push({ resolve }))),
    close: () => rl.close(),
  };
  return pipeLines;
}

async function askOne(question) {
  if (stdin.isTTY) {
    const rl = await createInterface({ input: stdin, output: stdout });
    try { return (await rl.question(question)).trim(); } finally { await rl.close(); }
  }
  if (question) process.stdout.write(question);
  return (await pipeLineReader().next()).trim();
}
function askClose() {
  if (pipeLines) { pipeLines.close(); pipeLines = null; }
}

// ---------------- 终端绘制小工具（对齐 z_ai coding-helper 的界面行为）----------------
// 宽度按终端列计（CJK 等宽字符记 2 列），行数把折行也算进去：
// 擦除按「屏幕行」上移，长状态行折行时也不会残留半截（inquirer 同款重绘口径）。

function stripAnsi(s) { return s.replace(/\x1b\[[0-9;?]*[A-Za-z]/g, ''); }

function textWidth(s) {
  let w = 0;
  for (const ch of stripAnsi(s)) {
    const c = ch.codePointAt(0);
    const wide = (c >= 0x1100 && c <= 0x115f) || (c >= 0x2e80 && c <= 0x303e)
      || (c >= 0x3041 && c <= 0x33ff) || (c >= 0x3400 && c <= 0x4dbf)
      || (c >= 0x4e00 && c <= 0x9fff) || (c >= 0xac00 && c <= 0xd7a3)
      || (c >= 0xf900 && c <= 0xfaff) || (c >= 0xfe30 && c <= 0xfe4f)
      || (c >= 0xff00 && c <= 0xff60) || (c >= 0xffe0 && c <= 0xffe6);
    w += wide ? 2 : 1;
  }
  return w;
}

function frameRows(text, cols) {
  return text.split('\n').reduce((n, line) => n + Math.max(1, Math.ceil(textWidth(line) / Math.max(1, cols))), 0);
}

// 与 console.clear() 等价（清屏、光标归位，保留滚动缓冲里的历史）
function clearScreen() { process.stdout.write('\x1b[2J\x1b[0f'); }

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------------- 方向键选择器（对齐 z_ai coding-helper：↑↓ 移动、回车确认）----------------
// 零依赖：TTY 下 raw mode + keypress；非 TTY（管道 / CI）退化为序号输入。
// 单选返回下标，多选返回下标数组，取消返回 null。
// 选中后整块菜单擦除、只收拢一行答案（对齐 inquirer），上一屏菜单不会残留。

function parsePickOne(ans, count) {
  if (ans === 'q') return null;
  const n = parseInt(ans, 10);
  return n >= 1 && n <= count ? n - 1 : 0;
}

function parsePickList(ans, count, preselected) {
  if (ans === 'q') return null;
  if (ans === '') return preselected && preselected.length ? [...preselected] : Array.from({ length: count }, (_, i) => i);
  if (ans === 'a') return Array.from({ length: count }, (_, i) => i);
  return [...new Set(ans.split(/[,，\s]+/).map((n) => parseInt(n, 10))
    .filter((n) => n >= 1 && n <= count).map((n) => n - 1))];
}

async function keySelect({ title, items, multi = false, checked = null }) {
  const hint = multi ? '（↑↓ 移动，空格 勾选，a 全选/清空，回车 确认，Ctrl+C 取消）' : '（↑↓ 移动，回车 确认，Ctrl+C 取消）';
  const labelLine = (it, i, cursor, sel) => {
    const cur = i === cursor ? '\x1b[36m❯\x1b[0m ' : '  ';
    const box = multi ? (sel.has(i) ? '\x1b[32m[x]\x1b[0m ' : '[ ] ') : '';
    return `${cur}${box}${it.label}${it.status ? `  \x1b[2m· ${it.status}\x1b[0m` : ''}`;
  };

  if (!stdin.isTTY) { // 非 TTY：序号选择退化
    log('\n' + title + hint);
    items.forEach((it, i) => log(`  ${i + 1}. ${it.label}${it.status ? ' · ' + it.status : ''}`));
    const ans = await askOne(multi ? '输入序号（逗号分隔，a=全部，q=取消）: ' : '输入序号（回车默认 1，q 取消）: ');
    if (multi) {
      const pre = checked ? items.map((it, i) => (checked(it) ? i : -1)).filter((i) => i >= 0) : null;
      return parsePickList(ans, items.length, pre);
    }
    return parsePickOne(ans, items.length);
  }

  const preRaw = stdin.isRaw;
  stdin.setRawMode(true);
  stdin.resume();
  emitKeypressEvents(stdin);
  let cursor = 0;
  let drewRows = 0;
  const sel = new Set(checked ? items.map((it, i) => (checked(it) ? i : -1)).filter((i) => i >= 0) : []);
  return await new Promise((resolve) => {
    // 帧末尾不写换行（光标停在帧最后一行）：帧底贴住终端末行时，
    // 带换行的重绘每按一次方向键就会把整屏向上滚一行。
    const erase = () => {
      if (drewRows <= 0) return;
      process.stdout.write('\r' + (drewRows > 1 ? `\x1b[${drewRows - 1}A` : '') + '\x1b[J');
    };
    const finish = (val) => {
      erase();
      stdin.setRawMode(preRaw === true);
      stdin.removeListener('keypress', onKey);
      stdin.pause();
      // 菜单收拢成一行答案（对齐 inquirer：选完菜单即消失）
      const label = (i) => String(items[i].label).trim();
      const answer = val === null ? '\x1b[2m已取消\x1b[0m'
        : multi ? (val.length ? val.map(label).join('、') : '\x1b[2m（未选）\x1b[0m')
        : label(val);
      process.stdout.write(`\x1b[36m❯\x1b[0m ${title.replace(/[：:]\s*$/, '')} \x1b[36m${answer}\x1b[0m\n`);
      resolve(val);
    };
    const draw = () => {
      const frame = title + ' \x1b[2m' + hint + '\x1b[0m\n' + items.map((it, i) => labelLine(it, i, cursor, sel)).join('\n');
      process.stdout.write(frame);
      drewRows = frameRows(frame, stdout.columns || 80);
    };
    const onKey = (str, key) => {
      if (!key) return;
      if (key.ctrl && key.name === 'c') return finish(null);
      if (key.name === 'up') cursor = (cursor - 1 + items.length) % items.length;
      else if (key.name === 'down') cursor = (cursor + 1) % items.length;
      else if (multi && key.name === 'space') { if (sel.has(cursor)) sel.delete(cursor); else sel.add(cursor); }
      else if (multi && (str === 'a' || str === 'A')) { if (sel.size === items.length) sel.clear(); else items.forEach((_, i) => sel.add(i)); }
      else if (key.name === 'return' || key.name === 'enter') return finish(multi ? [...sel].sort((a, b) => a - b) : cursor);
      else return; // 其余按键不重绘
      erase();
      draw();
    };
    stdin.on('keypress', onKey);
    draw();
  });
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
  const head = `model_provider = "${PROVIDER}"\nmodel = "${model}"\nmodel_reasoning_effort = "max"\nmodel_catalog_json = "~/.codex/models.json"\n`;
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

// ---------------- Codex ~/.codex/models.json 模型元数据 ----------------
// config.toml 的自定义 provider 只对终端 Codex CLI 生效；桌面端（ChatGPT.app 内置 Codex）
// 还要求 model_catalog_json 指向的 models.json 里存在模型元数据才能选用。
// 字段集对齐智谱 coding-helper 当前版本写入的最小合法模板（其 glm-5.3 条目同款）：
// 其中 shell_type / visibility / base_instructions 为必填，缺一即报
// “failed to parse model_catalog_json ... missing field”导致桌面端无法对话。
const CODEX_MODEL_MARKER = 'Huimu Engine model'; // 卸载时按此标记识别本站条目（卸载路径无 model 名）

function codexModelEntry(model) {
  return {
    slug: model,
    display_name: model,
    description: CODEX_MODEL_MARKER,
    default_reasoning_level: 'max',
    supported_reasoning_levels: [
      { effort: 'low', description: 'Light reasoning' },
      { effort: 'high', description: 'Enhanced reasoning' },
      { effort: 'max', description: 'Deep reasoning' },
    ],
    shell_type: 'shell_command',
    visibility: 'list',
    supported_in_api: true,
    priority: 0,
    base_instructions: '',
    supports_reasoning_summaries: true,
    default_reasoning_summary: 'none',
    support_verbosity: false,
    apply_patch_tool_type: 'freeform',
    truncation_policy: { mode: 'bytes', limit: 10000 },
    context_window: 1048576,
    max_context_window: 1048576,
    effective_context_window_percent: 95,
    supports_parallel_tool_calls: true,
    experimental_supported_tools: [],
    input_modalities: ['text'],
  };
}

function writeCodexModelsJson(modelsPath, model) {
  let config = { models: [] };
  try {
    if (existsSync(modelsPath)) {
      const parsed = JSON.parse(readFileSync(modelsPath, 'utf-8'));
      if (parsed && Array.isArray(parsed.models)) config = parsed;
    }
  } catch { /* 损坏文件按空配置重建 */ }
  // 去掉同 slug 旧条目与历史遗留的本站标记条目（换模型重装时旧条目一并清理）
  config.models = config.models.filter((m) => m.slug !== model && m.description !== CODEX_MODEL_MARKER);
  config.models.push(codexModelEntry(model));
  writeAtomic(modelsPath, JSON.stringify(config, null, 2) + '\n');
}

function removeCodexModelsJson(modelsPath) {
  if (!existsSync(modelsPath)) return;
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(modelsPath, 'utf-8'));
  } catch { return; }
  if (!parsed || !Array.isArray(parsed.models)) return;
  const kept = parsed.models.filter((m) => m.description !== CODEX_MODEL_MARKER);
  if (kept.length === 0) unlinkSync(modelsPath); // 只剩我们的条目 → 整个文件回收
  else if (kept.length !== parsed.models.length) writeAtomic(modelsPath, JSON.stringify({ ...parsed, models: kept }, null, 2) + '\n');
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
      // 安装会改动/删除非本站的键（ANTHROPIC_API_KEY 等冲突键、顶层 model）：
      // 改动前把原始值备份到这里，卸载时原样还原——「卸载回到装之前」才成立。
      // 重复安装不覆盖备份（保住的是最早一次安装前的真实原状）
      backupPath: join(home, '.huimu', 'claude-code-backup.json'),
      backupKeysEnv: ['ANTHROPIC_API_KEY', 'ANTHROPIC_MODEL', 'ANTHROPIC_SMALL_FAST_MODEL', 'NO_PROXY', 'no_proxy'],
      install() {
        const p = join(home, '.claude', 'settings.json');
        const cfg = readJSON(p);
        const env = { ...(cfg.env || {}) };
        // 首次安装才写备份：重装/升级不覆盖，保住最早一次安装前的真实原状
        if (!existsSync(this.backupPath)) {
          const backup = { env: {}, model: cfg.model };
          for (const k of this.backupKeysEnv) if (env[k] !== undefined) backup.env[k] = env[k];
          writeJSON(this.backupPath, backup);
        }
        // 清掉其它服务商残留的冲突键：ANTHROPIC_API_KEY 会触发「双 token」警告，
        // 旧模型名环境变量会盖掉下面的槽位映射
        for (const k of ['ANTHROPIC_API_KEY', 'ANTHROPIC_MODEL', 'ANTHROPIC_SMALL_FAST_MODEL']) delete env[k];
        // 网关主机加进 NO_PROXY：机器上若有 socks5 等系统代理，Node fetch 对其报
        // UnsupportedProxyProtocol（连不上 API）——本站流量必须直连不走代理
        const proxyHosts = [];
        try {
          const u = new URL(base);
          proxyHosts.push(u.hostname);
          if (u.host !== u.hostname) proxyHosts.push(u.host); // 带端口形式一并加，匹配更稳
        } catch { /* base 非法时跳过，不阻塞安装 */ }
        for (const k of ['NO_PROXY', 'no_proxy']) {
          const list = String(env[k] || '').split(',').map((s) => s.trim()).filter(Boolean);
          for (const h of proxyHosts) if (!list.includes(h)) list.push(h);
          if (list.length) env[k] = list.join(',');
        }
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
        // 顶层 model：启动横幅与 /model 默认值显示本站模型（残留的旧服务商
        // "model": "DeepSeek-V4-Pro" 会盖掉一切槽位映射，请求打到不存在的模型名）
        writeJSON(p, { ...cfg, env, model });
        // 跳过 Claude Code 首次启动向导（与智谱 helper 同法）
        const ob = join(home, '.claude.json');
        const obCfg = readJSON(ob);
        if (!obCfg.hasCompletedOnboarding) writeJSON(ob, { ...obCfg, hasCompletedOnboarding: true });
      },
      uninstall() {
        const p = join(home, '.claude', 'settings.json');
        if (!existsSync(p)) return;
        const cfg = readJSON(p);
        const env = { ...(cfg.env || {}) };
        for (const k of [
          'ANTHROPIC_AUTH_TOKEN', 'ANTHROPIC_BASE_URL', 'API_TIMEOUT_MS',
          'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC', 'CLAUDE_CODE_AUTO_COMPACT_WINDOW',
          'ANTHROPIC_DEFAULT_HAIKU_MODEL', 'ANTHROPIC_DEFAULT_SONNET_MODEL', 'ANTHROPIC_DEFAULT_OPUS_MODEL',
          'ANTHROPIC_API_KEY',
        ]) delete env[k];
        // NO_PROXY 只摘掉本站主机，用户自己的免代理条目保留
        let hosts = [];
        try {
          const u = new URL(base);
          hosts = [u.hostname, u.host];
        } catch { /* ignore */ }
        for (const k of ['NO_PROXY', 'no_proxy']) {
          if (!env[k]) continue;
          const kept = String(env[k]).split(',').map((s) => s.trim())
            .filter((s) => s && !hosts.includes(s));
          if (kept.length) env[k] = kept.join(',');
          else delete env[k];
        }
        const next = { ...cfg, env };
        delete next.model; // 顶层 model 为本站安装时写入，一并回收
        // 有备份则把安装前的原始键值原样放回（卸载 = 回到装之前）
        const backup = readJSON(this.backupPath);
        if (backup.env || backup.model !== undefined) {
          for (const [k, v] of Object.entries(backup.env || {})) env[k] = v;
          if (backup.model !== undefined) next.model = backup.model;
          try { unlinkSync(this.backupPath); } catch { /* ignore */ }
        }
        if (Object.keys(env).length === 0) delete next.env;
        writeJSON(p, next);
      },
      status() {
        const env = readJSON(join(home, '.claude', 'settings.json')).env || {};
        return env.ANTHROPIC_BASE_URL === base ? `已接入（模型 ${env.ANTHROPIC_DEFAULT_SONNET_MODEL || '?'}）`
          : env.ANTHROPIC_BASE_URL ? `已接入其它服务（${env.ANTHROPIC_BASE_URL}）` : '未配置';
      },
      installed() {
        return (readJSON(join(home, '.claude', 'settings.json')).env || {}).ANTHROPIC_BASE_URL === base;
      },
    },
    'codex': {
      name: 'Codex CLI',
      configPath: join(home, '.codex', 'config.toml'),
      install() {
        const p = join(home, '.codex', 'config.toml');
        const text = existsSync(p) ? readFileSync(p, 'utf-8') : '';
        writeAtomic(p, installCodexToml(text, { base, key, model }));
        writeCodexModelsJson(join(home, '.codex', 'models.json'), model); // 桌面端 Codex 识别模型用
      },
      uninstall() {
        const p = join(home, '.codex', 'config.toml');
        if (existsSync(p)) writeAtomic(p, uninstallCodexToml(readFileSync(p, 'utf-8')));
        removeCodexModelsJson(join(home, '.codex', 'models.json')); // 只清我们的模型条目
      },
      status() {
        const p = join(home, '.codex', 'config.toml');
        if (!existsSync(p)) return '未配置';
        const t = readFileSync(p, 'utf-8');
        if (!t.includes(`[model_providers.${PROVIDER}]`)) return '未配置';
        // 顶层 model 键在第一个表头之前；安装路径写的顶层键即当前生效模型
        const head = t.split(/\n\s*\[/)[0];
        const m = head.match(/^\s*model\s*=\s*"([^"]+)"/m);
        return m ? `已接入（模型 ${m[1]}）` : '已接入';
      },
      installed() {
        const p = join(home, '.codex', 'config.toml');
        return existsSync(p) && readFileSync(p, 'utf-8').includes(`[model_providers.${PROVIDER}]`);
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
      installed() {
        return !!(readJSON(join(home, '.config', 'opencode', 'opencode.json')).provider || {})[PROVIDER];
      },
    },
    'trae-agent': {
      name: 'Trae Agent',
      configPath: join(home, '.trae-agent', 'trae_config.yaml'),
      // 覆盖前把用户的原配置备份到这里，卸载还原（重装不覆盖备份，保住最早原状）
      backupPath: join(home, '.huimu', 'trae-agent-backup.yaml'),
      envName: 'TRAE_CONFIG_FILE',
      managedMark: 'managed by huimu-agent-helper',
      rc() { return shellRcPath(home); },
      install() {
        const p = this.configPath;
        // 非本站配置先备份（首次安装才写）：trae_config.yaml 若是用户手写的，卸载要能回到原状
        const cur = readText(p);
        if (cur && !cur.includes(this.managedMark) && !existsSync(this.backupPath)) {
          writeAtomic(this.backupPath, cur);
        }
        // 结构对齐官方 config.py 的强校验三段式（缺段/缺必填键直接 ConfigError）：
        //   model_providers：OpenAI 兼容网关 = provider: openai + base_url（官方 README 同款接法）
        //   models：temperature/top_p/top_k/parallel_tool_calls/max_retries 是无默认值必填，
        //           官方示例省了也会 TypeError，必须显式写全
        //   agents.trae_agent：max_steps 必填；enable_lakeview 默认 true，但不配 lakeview 段
        //           会直接 ConfigError，必须显式关掉。YAML 只允许空格缩进（不许 tab）
        writeAtomic(p, [
          `# ${this.managedMark}`,
          'model_providers:',
          `  ${PROVIDER}:`,
          `    api_key: ${JSON.stringify(key)}`,
          '    provider: openai',
          `    base_url: ${JSON.stringify(chatBase)}`,
          'models:',
          `  ${PROVIDER}_model:`,
          `    model: ${JSON.stringify(model)}`,
          `    model_provider: ${PROVIDER}`,
          '    temperature: 0.5',
          '    top_p: 1.0',
          '    top_k: 0',
          '    parallel_tool_calls: true',
          '    max_retries: 3',
          'agents:',
          '  trae_agent:',
          `    model: ${PROVIDER}_model`,
          '    max_steps: 200',
          '    enable_lakeview: false',
          '',
        ].join('\n'));
        // trae-cli 只认「当前目录的 trae_config.yaml」或 TRAE_CONFIG_FILE；写进 rc 才能随处可用
        addShellRcExport(this.rc(), this.envName, p);
      },
      uninstall() {
        if (existsSync(this.configPath)) {
          if (existsSync(this.backupPath)) {
            writeAtomic(this.configPath, readFileSync(this.backupPath, 'utf-8')); // 还原安装前的原配置
            unlinkSync(this.backupPath);
          } else {
            unlinkSync(this.configPath); // 本助手建的文件直接删
          }
        }
        removeShellRcExport(this.rc(), this.envName);
      },
      status() {
        const t = readText(this.configPath);
        if (!t) return '未配置';
        if (!t.includes(this.managedMark)) return '已接入其它服务';
        const m = t.match(/^ {4}model: "(.+)"$/m); // models 段里的对外模型名（带引号者）
        const envOk = readText(this.rc()).includes(`# >>> ${this.envName} huimu >>>`);
        return m ? `已接入（模型 ${m[1]}${envOk ? '' : `，${this.envName} 未写入 rc，请重装修复`}）` : '已接入';
      },
      installed() {
        return readText(this.configPath).includes(this.managedMark);
      },
      postInstallHint() {
        return [
          '  Trae Agent CLI 未发布 PyPI，全局安装（一次性，需 git 可访问 github）：',
          '    uv tool install --with docker --with pexpect --with unidiff "trae-agent @ git+https://github.com/bytedance/trae-agent"',
          '  （--with 补的三个包是上游打包缺陷：核心代码 import docker 但它只在 evaluation extras 里）',
          `  环境变量已写入 ${this.rc()}，新开终端（或 source）后生效`,
          '  验证：trae-cli show-config    使用：trae-cli run "任务" / trae-cli interactive',
        ].join('\n');
      },
    },
  };
}

const AGENT_IDS = ['claude-code', 'codex', 'opencode', 'trae-agent'];

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

async function resolveModel(flags, base, key) {
  if (flags.model) return flags.model;
  let models = [];
  try {
    models = await fetchModels(base, key);
  } catch (e) {
    warn(`${e.message}，模型列表不可用`);
  }
  if (models.length === 0) return askOne('模型名（如 deepseek-v4-flash）: ');
  if (flags.yes) return models[0]; // 免交互：默认取第一个
  const pick = await keySelect({ title: '选择默认模型：', items: models.map((m) => ({ label: m })) });
  return pick === null ? null : models[pick];
}

async function cmdInstall(flags, agents, base) {
  const key = flags.key || process.env.HUIMU_API_KEY;
  if (!key || !key.startsWith('sk-')) die('缺少有效密钥：请用 --key sk-... 指定（或设置 HUIMU_API_KEY 环境变量）');
  const model = await resolveModel(flags, base, key);
  if (!model) { warn('未选择模型，已取消。'); return; }
  const defs = agentDefs({ home: homedir(), base, key, model });
  log('');
  for (const id of agents) {
    const d = defs[id];
    try {
      d.install();
      ok(`✓ ${d.name} 已接入（模型 ${model}）→ ${d.configPath}`);
      if (typeof d.postInstallHint === 'function') log(d.postInstallHint());
    } catch (e) {
      warn(`✗ ${d.name} 安装失败：${e.message}`);
    }
  }
  log('\n完成。再次运行向导（无参数）可随时切换或卸载。');
}

function cmdUninstall(agents) {
  const defs = agentDefs({ home: homedir(), base: '', key: '', model: '' });
  for (const id of agents) {
    const d = defs[id];
    try {
      d.uninstall();
      ok(`✓ ${d.name} 已卸载本站配置（其余配置保留）`);
      if (typeof d.postUninstallHint === 'function') log(d.postUninstallHint());
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

// 交互式向导：状态总览 + 方向键选择 接入/卸载（一个脚本完成全部操作，无须再复制卸载命令）。
// 界面对齐 z_ai coding-helper：每轮操作前清屏重绘（旧菜单绝不残留），选中项收拢成一行。
async function wizard(flags, base) {
  let key = flags.key || process.env.HUIMU_API_KEY || '';
  let models = null; // 密钥校验后的模型缓存（null=未校验，校验失败保持 null 以便重试）
  for (;;) {
    clearScreen();
    log(`\x1b[36m慧沐引擎 · Agent 接入助手 v${VERSION}\x1b[0m \x1b[2m${base}\x1b[0m`);
    const defs = agentDefs({ home: homedir(), base, key: '', model: '' });
    const items = AGENT_IDS.map((id) => ({ id, label: defs[id].name.padEnd(14), status: defs[id].status() }));
    log('\n当前接入状态：');
    for (const id of AGENT_IDS) log(`  ${defs[id].name.padEnd(14)}${defs[id].status()}`);

    const op = await keySelect({
      title: '选择操作：',
      items: [{ label: '接入工具' }, { label: '卸载工具' }, { label: '刷新状态' }, { label: '退出' }],
    });
    if (op === null || op === 3) { log('再见。'); return; }
    if (op === 2) { models = null; continue; } // 刷新：清屏重绘即重新探测

    if (op === 1) { // 卸载：只预选已接入本站的工具，无须密钥
      const picks = await keySelect({ title: '要卸载的工具：', items, multi: true, checked: (it) => defs[it.id].installed() });
      if (picks === null || picks.length === 0) { log('未选择，返回。'); await sleep(500); continue; }
      log('');
      let hadFail = false;
      for (const i of picks) {
        const d = defs[items[i].id];
        try {
          d.uninstall();
          ok(`✓ ${d.name} 已卸载本站配置（其余配置保留）`);
          if (typeof d.postUninstallHint === 'function') log(d.postUninstallHint());
        } catch (e) {
          hadFail = true;
          warn(`✗ ${d.name} 卸载失败：${e.message}`);
        }
      }
      await (hadFail ? askOne('\n回车返回菜单… ') : sleep(800)); // 结果短暂停留后清屏，新状态在总览里可见
      continue;
    }

    // 接入
    if (!key.startsWith('sk-')) {
      key = await askOne('API 密钥（sk- 开头，管理台「我的密钥」创建，q 返回）: ');
      if (!key.startsWith('sk-')) { warn('密钥应以 sk- 开头，返回菜单。'); await sleep(800); continue; }
    }
    if (models === null) {
      try {
        models = await fetchModels(base, key);
        ok(`密钥有效，可用模型 ${models.length} 个`);
      } catch (e) {
        warn(`${e.message}（可稍后在「刷新状态」后重试，或直接手输模型名）`);
      }
    }
    const picks = await keySelect({ title: '要接入的工具：', items, multi: true }); // 不预选，由用户勾选
    if (picks === null || picks.length === 0) { log('未选择，返回。'); await sleep(500); continue; }
    let model = flags.model || '';
    if (!model) {
      if (models && models.length > 0) {
        const m = await keySelect({ title: '选择默认模型：', items: models.map((mm) => ({ label: mm })) });
        if (m === null) { log('未选择模型，返回。'); await sleep(500); continue; }
        model = models[m];
      } else {
        model = await askOne('模型名（如 deepseek-v4-flash）: ');
        if (!model) { warn('未输入模型，返回。'); await sleep(800); continue; }
      }
    }
    const adefs = agentDefs({ home: homedir(), base, key, model });
    log('');
    let hadFail = false;
    for (const i of picks) {
      const d = adefs[items[i].id];
      try {
        d.install();
        ok(`✓ ${d.name} 已接入（模型 ${model}）→ ${d.configPath}`);
        if (typeof d.postInstallHint === 'function') log(d.postInstallHint());
      } catch (e) {
        hadFail = true;
        warn(`✗ ${d.name} 安装失败：${e.message}`);
      }
    }
    await (hadFail ? askOne('\n回车返回菜单… ') : sleep(800));
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

  // claude-code 安装/卸载往返：预置其它服务商残留（旧模型名/双 token/系统代理）
  mkdirSync(join(home, '.claude'), { recursive: true });
  writeFileSync(join(home, '.claude', 'settings.json'), JSON.stringify({
    model: 'DeepSeek-V4-Pro',
    statusLine: { type: 'command', command: 'old-provider-statusline' },
    env: { ANTHROPIC_API_KEY: 'sk-old', ANTHROPIC_MODEL: 'DeepSeek-V4-Pro', NO_PROXY: 'localhost' },
  }));
  defs['claude-code'].install();
  const s1 = readJSON(join(home, '.claude', 'settings.json'));
  expect('claude env 注入', s1.env && s1.env.ANTHROPIC_BASE_URL === ctx.base && s1.env.ANTHROPIC_AUTH_TOKEN === ctx.key);
  expect('claude 冲突键清理', s1.env.ANTHROPIC_API_KEY === undefined && s1.env.ANTHROPIC_MODEL === undefined);
  expect('claude 顶层 model 改写', s1.model === ctx.model);
  expect('claude 用户键保留', s1.statusLine && s1.statusLine.command === 'old-provider-statusline');
  expect('claude NO_PROXY 追加网关主机', s1.env.NO_PROXY === 'localhost,gw.test,gw.test:8080');
  expect('claude onboarding 标记', readJSON(join(home, '.claude.json')).hasCompletedOnboarding === true);
  // 重装不覆盖备份：卸载一次要还原的是最早的原状，而不是上次卸载后的中间态
  defs['claude-code'].install();
  const backupPath = join(home, '.huimu', 'claude-code-backup.json');
  expect('claude 备份文件已建', existsSync(backupPath));
  defs['claude-code'].uninstall();
  const s2 = readJSON(join(home, '.claude', 'settings.json'));
  expect('claude 卸载还原旧服务商键',
    s2.env && s2.env.ANTHROPIC_API_KEY === 'sk-old' && s2.env.ANTHROPIC_MODEL === 'DeepSeek-V4-Pro');
  expect('claude 卸载还原顶层 model 与 NO_PROXY',
    s2.model === 'DeepSeek-V4-Pro' && s2.env.NO_PROXY === 'localhost');
  expect('claude 卸载保留用户键/清空本站键',
    s2.statusLine !== undefined && s2.env.ANTHROPIC_AUTH_TOKEN === undefined && s2.env.ANTHROPIC_BASE_URL === undefined);
  expect('claude 卸载后备份回收', !existsSync(backupPath));

  // codex toml：空文件安装、用户已有内容保留、幂等重装、卸载还原
  defs['codex'].install();
  const t1 = readFileSync(join(home, '.codex', 'config.toml'), 'utf-8');
  expect('codex 顶层键', t1.startsWith('model_provider = "huimu"\nmodel = "m-alpha"\nmodel_reasoning_effort = "max"\nmodel_catalog_json = "~/.codex/models.json"'));
  expect('codex provider 段', t1.includes('[model_providers.huimu]') && t1.includes('wire_api = "responses"'));
  expect('codex 状态带模型名', defs['codex'].status() === '已接入（模型 m-alpha）');
  const mj1 = readJSON(join(home, '.codex', 'models.json'));
  const me1 = mj1.models[0];
  expect('codex models.json 写入', mj1.models.length === 1 && me1.slug === 'm-alpha');
  expect('codex models.json 必填字段齐', me1.shell_type === 'shell_command' && me1.visibility === 'list'
    && typeof me1.base_instructions === 'string' && me1.description === 'Huimu Engine model');
  expect('codex models.json 推理档位', me1.supported_reasoning_levels.some((l) => l.effort === 'max'));
  writeFileSync(join(home, '.codex', 'config.toml'),
    '# 用户注释\nuser_key = 1\nmodel = "user-model"\n\n[other]\nx = 2\n');
  // 预置：他人条目 + 旧版本残留的缺字段本站条目（安装时应清理替换为新模板）
  writeFileSync(join(home, '.codex', 'models.json'),
    JSON.stringify({ models: [
      { slug: 'foreign', display_name: 'Foreign', description: 'user own' },
      { slug: 'stale-old', description: 'Huimu Engine model', input_modalities: ['text'] },
    ] }));
  defs['codex'].install();
  const t2 = readFileSync(join(home, '.codex', 'config.toml'), 'utf-8');
  expect('codex 用户配置保留', t2.includes('# 用户注释') && t2.includes('user_key = 1') && t2.includes('[other]'));
  expect('codex 用户顶层键不被删', t2.includes('user_key = 1'));
  expect('codex 我们顶层键前置', t2.indexOf('model_provider = "huimu"') === 0);
  const t3 = installCodexToml(t2, ctx); // 幂等
  expect('codex 幂等（唯一 provider 段）', (t3.match(/\[model_providers\.huimu\]/g) || []).length === 1);
  const mj2 = readJSON(join(home, '.codex', 'models.json'));
  expect('codex models.json 幂等且他人条目保留',
    mj2.models.length === 2 && mj2.models.some((m) => m.slug === 'foreign') && mj2.models.some((m) => m.slug === 'm-alpha'));
  expect('codex models.json 清理旧标记条目', !mj2.models.some((m) => m.slug === 'stale-old')
    && mj2.models.every((m) => m.slug !== 'm-alpha' || m.shell_type === 'shell_command'));
  defs['codex'].uninstall();
  const t4 = readFileSync(join(home, '.codex', 'config.toml'), 'utf-8');
  expect('codex 卸载还原', !t4.includes('huimu') && !t4.includes('"m-alpha"') && t4.includes('# 用户注释') && t4.includes('user_key = 1'));
  const mj3 = readJSON(join(home, '.codex', 'models.json'));
  expect('codex models.json 卸载只清本站条目', mj3.models.length === 1 && mj3.models[0].slug === 'foreign');
  // 用户自配 model（model_provider 非 huimu）时卸载不得回收
  writeFileSync(join(home, '.codex', 'config.toml'), 'model = "keep-me"\n');
  defs['codex'].uninstall();
  expect('codex 用户 model 保留', readFileSync(join(home, '.codex', 'config.toml'), 'utf-8').includes('keep-me'));
  // models.json 只剩本站条目时，卸载删除整个文件
  unlinkSync(join(home, '.codex', 'models.json'));
  defs['codex'].install();
  defs['codex'].uninstall();
  expect('codex models.json 空时整体回收', !existsSync(join(home, '.codex', 'models.json')));

  // opencode 往返
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

  // trae-agent：YAML 三段式 + shell rc 环境变量块；用户手写配置先备份、卸载还原
  const taDir = join(home, '.trae-agent');
  mkdirSync(taDir, { recursive: true });
  writeFileSync(join(taDir, 'trae_config.yaml'), '# user own config\nmodel_providers: {}\n');
  defs['trae-agent'].install();
  const ta1 = readFileSync(join(taDir, 'trae_config.yaml'), 'utf-8');
  expect('trae-agent YAML 三段式',
    ta1.includes('model_providers:') && ta1.includes('models:') && ta1.includes('agents:') && ta1.includes('  trae_agent:'));
  expect('trae-agent OpenAI 兼容接入',
    ta1.includes('provider: openai') && ta1.includes(`base_url: "http://gw.test:8080/v1"`) && ta1.includes(`api_key: "${ctx.key}"`));
  expect('trae-agent 模型与必填采样参数',
    ta1.includes(`model: "${ctx.model}"`) && ta1.includes('model_provider: huimu')
      && ta1.includes('temperature: 0.5') && ta1.includes('top_p: 1.0') && ta1.includes('top_k: 0')
      && ta1.includes('parallel_tool_calls: true') && ta1.includes('max_retries: 3'));
  expect('trae-agent agents 段必填 max_steps', /^ {4}max_steps: \d+$/m.test(ta1));
  expect('trae-agent 显式关 lakeview（默认 true 缺 lakeview 段会 ConfigError）', ta1.includes('enable_lakeview: false'));
  const taRc = shellRcPath(home);
  expect('trae-agent rc 环境变量块',
    readText(taRc).includes('export TRAE_CONFIG_FILE=') && readText(taRc).includes(join(taDir, 'trae_config.yaml')));
  expect('trae-agent 用户原配置已备份', existsSync(join(home, '.huimu', 'trae-agent-backup.yaml')));
  defs['trae-agent'].install(); // 幂等重装
  expect('trae-agent 重装 rc 块唯一',
    (readText(taRc).match(/# >>> TRAE_CONFIG_FILE huimu >>>/g) || []).length === 1);
  expect('trae-agent 状态', defs['trae-agent'].status() === `已接入（模型 ${ctx.model}）`);
  defs['trae-agent'].uninstall();
  expect('trae-agent 卸载还原用户配置',
    readFileSync(join(taDir, 'trae_config.yaml'), 'utf-8') === '# user own config\nmodel_providers: {}\n');
  expect('trae-agent 卸载清 rc 块与备份',
    !readText(taRc).includes('TRAE_CONFIG_FILE') && !existsSync(join(home, '.huimu', 'trae-agent-backup.yaml')));
  // 无用户配置时的干净安装：卸载直接删文件、rc 空文件回收
  unlinkSync(join(taDir, 'trae_config.yaml')); // 上一段还原的用户配置先清掉，才是「无外部配置」场景
  defs['trae-agent'].install();
  defs['trae-agent'].uninstall();
  expect('trae-agent 无备份时卸载删配置', !existsSync(join(taDir, 'trae_config.yaml')) && !existsSync(taRc));

  // 非 TTY 序号解析（选择器退化路径）
  expect('parsePickOne 序号与默认', parsePickOne('2', 4) === 1 && parsePickOne('', 4) === 0 && parsePickOne('q', 4) === null);
  expect('parsePickList 全选/子集/预选', JSON.stringify(parsePickList('a', 3)) === '[0,1,2]'
    && JSON.stringify(parsePickList('1,3', 3)) === '[0,2]'
    && JSON.stringify(parsePickList('', 3, [2])) === '[2]'
    && parsePickList('q', 3) === null);

  // 终端宽度 / 折行行数（菜单擦除口径：CJK 记 2 列、ANSI 转义不计宽）
  expect('textWidth 中英混排', textWidth('中文ab') === 6 && textWidth('\x1b[36m中文\x1b[0m') === 4);
  expect('frameRows 折行计数', frameRows('ab\ncd', 10) === 2 && frameRows('a'.repeat(200), 80) === 3
    && frameRows('中'.repeat(80), 80) === 2 && frameRows('', 80) === 1);

  if (failed > 0) die(`\n自检失败 ${failed} 项`);
  ok('\n自检全部通过');
}

// ---------------- 入口 ----------------
async function main() {
  const argv = process.argv.slice(2);
  const { command, flags } = parseArgs(argv);

  if (command === 'selftest') return selftest();

  const base = resolveBase(flags);
  try {
    if (command === 'status') return cmdStatus(base);

    if (command === 'install') {
      const agents = flags._.filter((a) => AGENT_IDS.includes(a));
      if (agents.length === 0) die(`请指定要接入的 agent：${AGENT_IDS.join(' / ')}（或多个空格分隔）`);
      const bad = flags._.filter((a) => !AGENT_IDS.includes(a));
      if (bad.length) warn(`忽略不支持的 agent：${bad.join(', ')}（支持：${AGENT_IDS.join(' / ')}）`);
      return await cmdInstall(flags, agents, base);
    }
    if (command === 'uninstall') {
      if (flags._.includes('all')) return cmdUninstall(AGENT_IDS);
      const agents = flags._.filter((a) => AGENT_IDS.includes(a));
      if (agents.length === 0) die(`请指定要卸载的 agent：${AGENT_IDS.join(' / ')}，或 uninstall all`);
      return cmdUninstall(agents);
    }
    if (command && command !== 'wizard') die(`未知命令：${command}\n用法：helper.mjs [install|uninstall|status|selftest] [agent...] [--base --key --model --yes]`);
    return await wizard(flags, base);
  } finally {
    askClose();
  }
}

main().catch((e) => die(e.message));
