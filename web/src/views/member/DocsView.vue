<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiMyModels, apiMyKeys } from '../../api/member'

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

function copy(text: string) {
  navigator.clipboard.writeText(text).then(() => ElMessage.success('已复制'))
}
</script>

<template>
  <el-card shadow="never">
    <template #header>接入文档（OpenAI 兼容）</template>

    <el-alert type="info" :closable="false" style="margin-bottom: 16px"
      title="中转站完全兼容 OpenAI API 格式：任何支持自定义 base_url 的 SDK/工具都能直接使用。" />

    <h3>1. 基础信息</h3>
    <el-descriptions :column="1" border style="max-width: 700px">
      <el-descriptions-item label="Base URL"><code>{{ baseURL }}</code></el-descriptions-item>
      <el-descriptions-item label="鉴权方式">请求头 <code>Authorization: Bearer sk-你的完整密钥</code></el-descriptions-item>
      <el-descriptions-item label="对话接口"><code>POST {{ baseURL }}/chat/completions</code>（支持 stream）</el-descriptions-item>
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

    <h3>6. 常见问题</h3>
    <ul class="faq">
      <li><b>403 model_not_allowed</b>：该模型未被授权，联系公司管理员在「员工管理 → 模型授权」中勾选。</li>
      <li><b>429 insufficient_balance</b>：个人或公司额度已耗尽，发起额度申请或联系管理员。</li>
      <li><b>429 monthly_limit_exceeded</b>：当月消费已达单月上限，次月自动恢复。</li>
      <li><b>429 rate_limit_error</b>：请求过于频繁（每密钥默认 60 次/分钟）。</li>
      <li><b>流式响应</b>：中转站会自动向上游请求 usage 统计用于计费，无需客户端做任何改动。</li>
    </ul>
  </el-card>
</template>

<style scoped>
h3 { margin: 20px 0 10px; color: #303133; }
.code-block { position: relative; max-width: 760px; }
.code-block pre {
  background: #0d1117; color: #e6edf3; padding: 14px 16px; border-radius: 6px;
  overflow-x: auto; font-size: 12.5px; line-height: 1.6; margin: 0;
}
.copy-btn { position: absolute; top: 8px; right: 8px; }
.faq { color: #606266; line-height: 2; padding-left: 20px; }
</style>
