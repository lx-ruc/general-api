<script setup lang="ts">
import { computed, ref } from 'vue'
import CodeBlock from './CodeBlock.vue'

// Agent 一键接入助手：网关托管的零依赖 Node 脚本（与智谱 coding-helper 同款体验）
const helperCmd = computed(() => `sh -c "$(curl -fsSL ${location.origin}/agent-helper)"`)
const helperYesCmd = computed(
  () => `sh -c "$(curl -fsSL ${location.origin}/agent-helper)" install claude-code codex --key {你的密钥} --model {你的模型} --yes`,
)
const helperStatusCmd = computed(() => `sh -c "$(curl -fsSL ${location.origin}/agent-helper)" status`)
const helperUninstallCmd = computed(
  () => `sh -c "$(curl -fsSL ${location.origin}/agent-helper)" uninstall all`,
)

// ---- 六款工具手动配置（与一键助手写入的内容一致，占位符按实际值替换） ----
const baseURL = `${location.origin}/v1`

const claudeExample = computed(() => `# ~/.claude/settings.json
{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "{你的密钥}",
    "ANTHROPIC_BASE_URL": "${location.origin}",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "{你的模型}",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "{你的模型}",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "{你的模型}"
  }
}`)

const codexExample = computed(() => `# ~/.codex/config.toml
model_provider = "huimu"
model = "{你的模型}"
model_reasoning_effort = "max"
model_catalog_json = "~/.codex/models.json"

[model_providers.huimu]
name = "huimu"
base_url = "${baseURL}"
env_key = "HUIMU_API_KEY"   # export HUIMU_API_KEY={你的密钥}
wire_api = "responses"`)

const opencodeExample = computed(() => `# ~/.config/opencode/opencode.json
{
  "provider": {
    "huimu": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "huimu",
      "options": { "baseURL": "${baseURL}", "apiKey": "{你的密钥}" },
      "models": [{ "name": "{你的模型}" }]
    }
  },
  "model": "huimu/{你的模型}",
  "small_model": "huimu/{你的模型}"
}`)

const crushExample = computed(() => `# ~/.config/crush/crush.json
{
  "providers": {
    "huimu": {
      "id": "huimu",
      "name": "Huimu",
      "base_url": "${baseURL}",
      "api_key": "{你的密钥}"
    }
  }
}`)

const factoryDroidExample = computed(() => `# ~/.factory/settings.json
{
  "customModels": [
    {
      "displayName": "Huimu Engine [{你的模型}] - Openai",
      "model": "{你的模型}",
      "baseUrl": "${baseURL}",
      "apiKey": "{你的密钥}",
      "provider": "generic-chat-completion-api",
      "maxOutputTokens": 131072
    }
  ]
}`)

// Trae 不开放可写的模型配置文件：在 IDE 内登记（一键助手会写入标记并打印同样的指引）
const traeExample = computed(() => `入口：设置 → 模型 → 添加模型 → 自定义配置

API 地址：${baseURL}（开启「完整 URL」开关时填 ${baseURL}/chat/completions）
API Key：{你的密钥}
模型 ID：{你的模型}`)

// 手动配置 tab：与一键助手覆盖的六款工具一一对应
interface ManualTab {
  key: string
  label: string
  code: string
  note: string
  lang: string
}

const manualTabs = computed<ManualTab[]>(() => [
  {
    key: 'claude-code',
    label: 'Claude Code',
    code: claudeExample.value,
    lang: 'json',
    note: '本站原生兼容 Anthropic Messages 协议（/v1/messages），无须任何协议转换即可直连。要点：ANTHROPIC_BASE_URL 填网关根地址（不带 /v1，Claude Code 自行拼接）；三个 ANTHROPIC_DEFAULT_*_MODEL 把 sonnet / opus / haiku 槽位都映射到本站模型；密钥就是你的 sk- 密钥（ANTHROPIC_AUTH_TOKEN）。',
  },
  {
    key: 'codex',
    label: 'Codex CLI',
    code: codexExample.value,
    lang: 'toml',
    note: '自定义 provider 走本站 /v1/responses（OpenAI Responses 协议）——Codex CLI 0.142 起已移除 wire_api = "chat"，必须用 responses；密钥经环境变量 HUIMU_API_KEY 注入（写在 shell 配置里 export）。桌面端（ChatGPT 内置 Codex）还需 ~/.codex/models.json 模型元数据（model_catalog_json 指向），一键助手会自动生成，建议直接使用。',
  },
  {
    key: 'opencode',
    label: 'OpenCode',
    code: opencodeExample.value,
    lang: 'json',
    note: 'provider 用 @ai-sdk/openai-compatible 适配器；顶层 model / small_model 设为 huimu/{你的模型} 即默认使用本站。',
  },
  {
    key: 'crush',
    label: 'Crush',
    code: crushExample.value,
    lang: 'json',
    note: '在 providers 里加一个 huimu 条目，随后在 Crush 的模型选择里切到 Huimu。',
  },
  {
    key: 'factory-droid',
    label: 'Factory Droid',
    code: factoryDroidExample.value,
    lang: 'json',
    note: 'customModels 增加一条 generic-chat-completion-api 连接；displayName 含 "Huimu" 便于一键助手识别与更新。',
  },
  {
    key: 'trae',
    label: 'Trae',
    code: traeExample.value,
    lang: 'text',
    note: 'Trae 的自定义模型只开放 IDE 内登记（无可写配置文件），按左侧信息在 设置 → 模型 → 添加模型 → 自定义配置 中填写即可；一键助手会写入 ~/.trae/huimu.json 标记并打印同样指引。',
  },
])
const tab = ref('claude-code')
const currentTab = computed(() => manualTabs.value.find((t) => t.key === tab.value) || manualTabs.value[0])
</script>

<template>
  <article class="doc-article">
    <div class="doc-kicker">功能指南</div>
    <h1 class="doc-h1">Agent 一键接入</h1>
    <p class="doc-desc">一条命令把命令行 Agent 接入本站：自动校验密钥、拉取模型列表、写入各工具配置。与智谱 coding-helper 同款交互（方向键选择），接入与卸载都在向导里完成。</p>

    <div class="doc-prose">
      <h2>一键命令</h2>
      <p>准备一枚 <code>sk-</code> 密钥（管理台【我的密钥】创建），在终端执行：</p>
      <CodeBlock :code="helperCmd" lang="bash" />
      <p>向导启动后先展示各工具的接入状态，再用方向键选择操作：<strong>接入工具 / 卸载工具 / 刷新状态 / 退出</strong>——接入与卸载在同一个向导里完成，无须再复制其它命令。接入时依次引导：粘贴密钥（即时校验）→ 勾选工具（空格多选）→ 从你的已授权模型中选择默认模型 → 写入配置。需要 <strong>Node.js ≥ 18</strong> 与 curl，无需其它依赖。</p>

      <div class="doc-callout info">
        <div class="doc-callout-title">安全与可逆</div>
        <p>脚本由本站托管下发，全程只读写你本机的工具配置文件；安装只增改本站相关的配置键、不动其它设置；
          卸载按同样的边界精确还原。密钥只写入本机配置文件，不经过第三方。</p>
      </div>

      <h2>支持的六款工具</h2>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th>工具</th><th>配置文件</th><th>接入方式</th></tr>
          </thead>
          <tbody>
            <tr>
              <td><strong>Claude Code</strong></td>
              <td><code>~/.claude/settings.json</code></td>
              <td>Anthropic 协议直连（<code>/v1/messages</code>），env 注入模型映射</td>
            </tr>
            <tr>
              <td><strong>Codex CLI</strong></td>
              <td><code>~/.codex/config.toml</code> + <code>models.json</code></td>
              <td>自定义 provider，<code>wire_api = "responses"</code>（走 <code>/v1/responses</code>）；桌面端另配模型元数据</td>
            </tr>
            <tr>
              <td><strong>OpenCode</strong></td>
              <td><code>~/.config/opencode/opencode.json</code></td>
              <td><code>@ai-sdk/openai-compatible</code> provider</td>
            </tr>
            <tr>
              <td><strong>Crush</strong></td>
              <td><code>~/.config/crush/crush.json</code></td>
              <td><code>providers</code> 增加 huimu 条目</td>
            </tr>
            <tr>
              <td><strong>Factory Droid</strong></td>
              <td><code>~/.factory/settings.json</code></td>
              <td><code>customModels</code> 增加 generic-chat-completion-api 连接</td>
            </tr>
            <tr>
              <td><strong>Trae</strong></td>
              <td><code>~/.trae/huimu.json</code>（标记）</td>
              <td>IDE 内登记（设置 → 模型 → 自定义配置），助手打印指引</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p>前五款工具的接入清单与智谱 coding-helper 保持一致，另加 Trae（IDE 内登记）；重复执行安装命令是幂等的（覆盖更新本站配置，保留你的其它设置）。</p>

      <h2>免交互安装与日常管理</h2>
      <p>CI / 脚本场景跳过向导，一步到位：</p>
      <CodeBlock :code="helperYesCmd" lang="bash" />
      <p>查看接入状态、卸载本站配置（保留各工具的其它配置）：</p>
      <CodeBlock :code="helperStatusCmd" lang="bash" />
      <CodeBlock :code="helperUninstallCmd" lang="bash" />
      <p>子命令与参数：<code>install &lt;agent…&gt;</code>（安装指定工具，多个空格分隔）、<code>uninstall &lt;agent… | all&gt;</code>、
        <code>status</code>、<code>selftest</code>（自检）；<code>--key</code> / <code>--model</code> / <code>--yes</code> 为免交互参数，
        <code>--base</code> 一般不需要（从网关入口运行时自动注入地址）。</p>

      <h2>手动配置</h2>
      <p>不想用脚本也可以手动配置——以下示例与一键助手写入的内容一致，占位符按你的实际值替换：</p>
      <div class="doc-tabs">
        <button
          v-for="t in manualTabs" :key="t.key" type="button"
          class="doc-tab" :class="{ on: tab === t.key }" @click="tab = t.key">
          {{ t.label }}
        </button>
      </div>
      <CodeBlock :code="currentTab.code" :lang="currentTab.lang" />
      <p class="tab-note">{{ currentTab.note }}</p>

      <h2>常见问题</h2>
      <ul>
        <li><strong>密钥校验失败</strong> → 确认密钥完整复制（<code>sk-</code> 开头）且账号状态正常；也可先用 <code>curl {{ baseURL }}/models -H "Authorization: Bearer {你的密钥}"</code> 自查。</li>
        <li><strong>模型列表为空</strong> → 你还没有被授权任何模型，联系客户管理员在「模型授权」中勾选。</li>
        <li><strong>已有其它服务的配置会冲突吗</strong> → 不会互相破坏：本站配置写在自己的键位 / 表段里；同一工具切换服务商时，以其当前生效的配置为准（如 Claude Code 的 env、Codex 的 model_provider）。</li>
        <li><strong>工具调用（Function Calling）</strong> → 对话端点完整透传 messages / tools / tool_choice；<code>/v1/messages</code> 同样完整支持 tools / tool_result 往返，Agent 场景可正常使用。</li>
        <li><strong>计费与限额</strong> → Agent 调用与普通 API 完全同一链路：按模型输入 / 输出单价计量，受密钥限流与双层额度约束，调用明细可在「用量」中查询。</li>
      </ul>
    </div>
  </article>
</template>

<style scoped>
.tab-note { font-size: 12.5px; color: var(--tg-muted); }
</style>
