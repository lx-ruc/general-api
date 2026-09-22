<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiMyModels, apiMyKeys } from '../../api/member'
import { copyText } from '../../utils/clipboard'

const models = ref<any[]>([])
const keys = ref<any[]>([])
const baseURL = `${location.origin}/v1`

onMounted(async () => {
  const m = await apiMyModels()
  models.value = m.models || []
  keys.value = await apiMyKeys()
})

const exampleKey = computed(() => {
  const prefix = keys.value[0]?.key_prefix
  return prefix ? `${prefix}<你的完整密钥>` : 'sk-<在「我的密钥」新建后填入>'
})
const firstModel = computed(() => models.value[0]?.name || 'deepseek-chat')
// Agent 配置示例里的模型清单（至多前 6 个，够覆盖又不冗长）
const modelList = computed(() => {
  const names = models.value.slice(0, 6).map((m) => m.name)
  return names.length > 0 ? names : [firstModel.value]
})

const curlExample = computed(() => `curl ${baseURL}/chat/completions \\
  -H "Authorization: Bearer ${exampleKey.value}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${firstModel.value}",
    "messages": [{"role": "user", "content": "你好"}]
  }'`)

const curlStreamExample = computed(() => `curl -N ${baseURL}/chat/completions \\
  -H "Authorization: Bearer ${exampleKey.value}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${firstModel.value}",
    "stream": true,
    "messages": [{"role": "user", "content": "给我讲个故事"}]
  }'`)

const pythonExample = computed(() => `from openai import OpenAI

client = OpenAI(
    api_key="sk-你的完整密钥",
    base_url="${baseURL}",
)

resp = client.chat.completions.create(
    model="${firstModel.value}",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)`)

const jsExample = computed(() => `import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-你的完整密钥",
  baseURL: "${baseURL}",
});

const resp = await client.chat.completions.create({
  model: "${firstModel.value}",
  messages: [{ role: "user", content: "你好" }],
});
console.log(resp.choices[0].message.content);`)

// ---- Agent / 编程工具接入 ----
// Codex CLI：~/.codex/config.toml 自定义 provider（wire_api=chat 即 OpenAI 对话格式）
const codexExample = computed(() => `# ~/.codex/config.toml
model = "${firstModel.value}"
model_provider = "huimu"

[model_providers.huimu]
name = "huimu"
base_url = "${baseURL}"
env_key = "HUIMU_API_KEY"   # export HUIMU_API_KEY=sk-你的完整密钥
wire_api = "chat"`)

// Continue：~/.continue/config.yaml（旧版为 config.json 的 models 数组，字段同名）
const continueExample = computed(() => `# ~/.continue/config.yaml
name: huimu
version: 1.0.0
models:
  - name: ${firstModel.value}
    provider: openai
    model: ${firstModel.value}
    apiBase: ${baseURL}
    apiKey: sk-你的完整密钥
    roles: [chat, edit, apply]`)

// Claude Code：原生说 Anthropic 协议，须经 claude-code-router 转成 OpenAI 格式再进本站
const ccrExample = computed(() => `# 1) 安装：npm install -g @musistudio/claude-code-router
# 2) 写入 ~/.claude-code-router/config.json（api_base_url 为完整对话端点）：
{
  "Providers": [
    {
      "name": "huimu",
      "api_base_url": "${baseURL}/chat/completions",
      "api_key": "sk-你的完整密钥",
      "models": [${modelList.value.map((n) => `"${n}"`).join(', ')}],
      "transformer": { "use": ["openai"] }
    }
  ],
  "Router": { "default": "huimu,${firstModel.value}" }
}
# 3) 用 ccr code 启动（代替 claude），即走本站转发`)

// Open WebUI / Dify 等平台：管理员设置里加 OpenAI 兼容连接
const platformExample = computed(() => `平台类型：OpenAI API Compatible
API 地址（Base URL）：${baseURL}
API Key：sk-你的完整密钥
模型名（Model ID）：${firstModel.value}
（Open WebUI：设置 → 连接 → OpenAI API；Dify：设置 → 模型供应商 → OpenAI-API-compatible）`)

async function copy(text: string) {
  const ok = await copyText(text)
  if (ok) ElMessage.success('已复制')
  else ElMessage.warning('复制失败，请手动选择复制')
}
</script>

<template>
  <el-card shadow="never">
    <template #header>使用说明（OpenAI 兼容）</template>

    <el-alert type="info" :closable="false" style="margin-bottom: 16px"
      title="本站完全兼容 OpenAI API 格式：任何支持自定义 base_url 的 SDK、工具与 Agent 都能直接使用。" />

    <h3>快速上手（3 步）</h3>
    <ol class="steps">
      <li>在 <b>「我的密钥」</b>创建 API 密钥（<code>sk-</code> 开头，仅创建时完整显示一次，请妥善保存）。</li>
      <li>在你的工具里填入 Base URL <code>{{ baseURL }}</code> 与该密钥（鉴权头 <code>Authorization: Bearer sk-…</code>）。</li>
      <li>模型名从下方「可用模型」中选一个填入（未列出的模型未获授权，请联系客户管理员开通）。</li>
    </ol>

    <h3>1. 基础信息</h3>
    <el-descriptions :column="1" border style="max-width: 700px">
      <el-descriptions-item label="Base URL"><code>{{ baseURL }}</code></el-descriptions-item>
      <el-descriptions-item label="鉴权方式">请求头 <code>Authorization: Bearer sk-你的完整密钥</code></el-descriptions-item>
      <el-descriptions-item label="对话接口"><code>POST {{ baseURL }}/chat/completions</code>（支持 stream）</el-descriptions-item>
      <el-descriptions-item label="向量接口"><code>POST {{ baseURL }}/embeddings</code></el-descriptions-item>
      <el-descriptions-item label="模型列表"><code>GET {{ baseURL }}/models</code></el-descriptions-item>
      <el-descriptions-item label="可用模型">
        <el-tag v-for="m in models" :key="m.name" style="margin: 2px">{{ m.name }}</el-tag>
      </el-descriptions-item>
    </el-descriptions>

    <h3>2. curl 示例</h3>
    <div class="code-block">
      <pre>{{ curlExample }}</pre>
      <el-button size="small" class="copy-btn" @click="copy(curlExample)">复制</el-button>
    </div>

    <h3>3. 流式（SSE）示例</h3>
    <div class="code-block">
      <pre>{{ curlStreamExample }}</pre>
      <el-button size="small" class="copy-btn" @click="copy(curlStreamExample)">复制</el-button>
    </div>

    <h3>4. Python（openai SDK）</h3>
    <div class="code-block">
      <pre>{{ pythonExample }}</pre>
      <el-button size="small" class="copy-btn" @click="copy(pythonExample)">复制</el-button>
    </div>

    <h3>5. JavaScript / TypeScript（openai SDK）</h3>
    <div class="code-block">
      <pre>{{ jsExample }}</pre>
      <el-button size="small" class="copy-btn" @click="copy(jsExample)">复制</el-button>
    </div>

    <h3>6. Agent 与编程工具接入</h3>
    <p class="dim">以下工具都支持 OpenAI 兼容接口：把 Base URL 换成本站、密钥换成你的 <code>sk-</code> 密钥即可。配置里的占位符按你的实际值替换。</p>

    <el-collapse class="agents">
      <el-collapse-item name="cursor">
        <template #title><b>Cursor</b>（IDE）</template>
        <p>Settings → Models → 开启 OpenAI API Key：<b>API Key</b> 填你的密钥，<b>Override OpenAI Base URL</b> 填 <code>{{ baseURL }}</code>，保存后即可在模型列表选用 <code>{{ firstModel }}</code>。自定义接口不支持的部分功能（如 Tab 补全云托管）属 Cursor 自身限制。</p>
      </el-collapse-item>

      <el-collapse-item name="cline">
        <template #title><b>Cline / Roo Code</b>（VS Code 插件）</template>
        <p>插件设置里 API Provider 选 <b>OpenAI Compatible</b>：</p>
        <ul class="steps">
          <li>Base URL：<code>{{ baseURL }}</code></li>
          <li>API Key：<code>{{ exampleKey }}</code></li>
          <li>Model ID：<code>{{ firstModel }}</code>（或「可用模型」中任意一个）</li>
        </ul>
      </el-collapse-item>

      <el-collapse-item name="codex">
        <template #title><b>Codex CLI</b>（OpenAI 官方命令行）</template>
        <div class="code-block">
          <pre>{{ codexExample }}</pre>
          <el-button size="small" class="copy-btn" @click="copy(codexExample)">复制</el-button>
        </div>
      </el-collapse-item>

      <el-collapse-item name="continue">
        <template #title><b>Continue</b>（VS Code / JetBrains）</template>
        <div class="code-block">
          <pre>{{ continueExample }}</pre>
          <el-button size="small" class="copy-btn" @click="copy(continueExample)">复制</el-button>
        </div>
      </el-collapse-item>

      <el-collapse-item name="claude-code">
        <template #title><b>Claude Code</b>（须经协议转换）</template>
        <p>Claude Code 原生使用 Anthropic Messages 协议（<code>/v1/messages</code>），本站是 OpenAI 兼容协议，直连不通——用开源的 <b>claude-code-router</b> 在本地把请求转成 OpenAI 格式再进本站：</p>
        <div class="code-block">
          <pre>{{ ccrExample }}</pre>
          <el-button size="small" class="copy-btn" @click="copy(ccrExample)">复制</el-button>
        </div>
        <p class="dim">Router 里 <code>default</code> 的格式为 <code>provider名,模型名</code>，须与 Providers 里的 name 一致。</p>
      </el-collapse-item>

      <el-collapse-item name="platforms">
        <template #title><b>Open WebUI / Dify / FastGPT / LobeChat</b> 等平台</template>
        <p>在平台的管理设置里添加 OpenAI 兼容模型连接，按下面信息填写：</p>
        <div class="code-block">
          <pre>{{ platformExample }}</pre>
          <el-button size="small" class="copy-btn" @click="copy(platformExample)">复制</el-button>
        </div>
      </el-collapse-item>
    </el-collapse>

    <h3>7. 常见问题</h3>
    <ul class="faq">
      <li><b>403 model_not_allowed</b>：该模型未被授权，联系客户管理员在「子账号管理 → 模型授权」中勾选。</li>
      <li><b>429 insufficient_balance</b>：个人或客户额度已耗尽，发起额度申请或联系管理员。</li>
      <li><b>429 monthly_limit_exceeded</b>：当月消费已达单月上限，次月自动恢复。</li>
      <li><b>429 rate_limit_error</b>：请求过于频繁（每密钥默认 60 次/分钟）。</li>
      <li><b>流式响应</b>：本站会自动向上游请求 usage 统计用于计费，无需客户端做任何改动。</li>
      <li><b>计费口径</b>：按模型的输入/输出单价（元/百万 tokens）计费；上游提示缓存命中的输入部分按更低的「缓存命中单价」计（如有配置）。</li>
      <li><b>Agent 工具调用</b>：对话端点完整透传 messages / tools / tool_choice 等字段，支持 Function Calling 的模型即可正常使用。</li>
    </ul>
  </el-card>
</template>

<style scoped>
h3 { margin: 20px 0 10px; color: #303133; }
.steps { color: #606266; line-height: 2; padding-left: 20px; }
.dim { color: #909399; font-size: 12.5px; }
.code-block { position: relative; max-width: 760px; }
.code-block pre {
  background: #0d1117; color: #e6edf3; padding: 14px 16px; border-radius: 6px;
  overflow-x: auto; font-size: 12.5px; line-height: 1.6; margin: 0;
}
.copy-btn { position: absolute; top: 8px; right: 8px; }
.faq { color: #606266; line-height: 2; padding-left: 20px; }
.agents { max-width: 820px; margin-top: 4px; }
.agents p { color: #606266; line-height: 1.9; margin: 8px 0; }
</style>
