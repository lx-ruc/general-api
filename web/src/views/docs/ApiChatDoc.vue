<script setup lang="ts">
import { computed, ref } from 'vue'
import CodeBlock from './CodeBlock.vue'

// 对话补全接口文档（apifox 风格）：参数表 + 多语言示例 + 按状态码分组的响应示例
const baseURL = `${location.origin}/v1`
const endpoint = `${baseURL}/chat/completions`

const exTab = ref('curl')
const examples = computed<Record<string, { lang: string; code: string }>>(() => ({
  curl: {
    lang: 'bash',
    code: `curl ${endpoint} \\
  -H "Authorization: Bearer sk-你的密钥" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "deepseek-chat",
    "messages": [{"role": "user", "content": "你好"}]
  }'`,
  },
  stream: {
    lang: 'bash',
    code: `# 流式：SSE 逐块返回，curl 需加 -N 关闭缓冲
curl -N ${endpoint} \\
  -H "Authorization: Bearer sk-你的密钥" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "deepseek-chat",
    "stream": true,
    "messages": [{"role": "user", "content": "给我讲个故事"}]
  }'`,
  },
  python: {
    lang: 'python',
    code: `from openai import OpenAI

client = OpenAI(api_key="sk-你的密钥", base_url="${baseURL}")

# 非流式
resp = client.chat.completions.create(
    model="deepseek-chat",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)

# 流式
stream = client.chat.completions.create(
    model="deepseek-chat",
    stream=True,
    messages=[{"role": "user", "content": "给我讲个故事"}],
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)`,
  },
  node: {
    lang: 'javascript',
    code: `import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "sk-你的密钥",
  baseURL: "${baseURL}",
});

// 流式
const stream = await client.chat.completions.create({
  model: "deepseek-chat",
  stream: true,
  messages: [{ role: "user", content: "给我讲个故事" }],
});
for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content || "");
}`,
  },
}))
</script>

<template>
  <article class="doc-article">
    <div class="doc-kicker">API 参考</div>
    <h1 class="doc-h1">对话补全</h1>
    <p class="doc-desc">创建一次对话补全。与 OpenAI Chat Completions 协议完全兼容，请求体原样透传所选上游。</p>

    <div class="doc-endpoint">
      <span class="doc-method post">POST</span>
      <span class="doc-path">/v1/chat/completions</span>
      <span class="doc-base">{{ baseURL }}</span>
    </div>

    <div class="doc-prose">
      <h2>鉴权</h2>
      <p>HTTP Header 携带 <code>Authorization: Bearer sk-你的密钥</code>。模型必须在该密钥的授权列表内，
        否则返回 <code>403 model_not_allowed</code>。</p>

      <h2>Body 参数（application/json）</h2>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th style="width: 150px">参数</th><th style="width: 90px">类型</th><th style="width: 70px">必填</th><th>说明</th></tr>
          </thead>
          <tbody>
            <tr><td><code>model</code></td><td>string</td><td><span class="doc-req">必填</span></td>
              <td>模型名，以 <code>GET /v1/models</code> 返回为准（如 <code>deepseek-chat</code>）。</td></tr>
            <tr><td><code>messages</code></td><td>array</td><td><span class="doc-req">必填</span></td>
              <td>对话消息列表，按序组成上下文。元素结构见下表。</td></tr>
            <tr><td><code>stream</code></td><td>boolean</td><td><span class="doc-opt">可选</span></td>
              <td>默认 <code>false</code>。<code>true</code> 时以 SSE 流式返回，末块带 <code>usage</code>
                （网关自动注入 <code>stream_options.include_usage</code> 保证计费）。</td></tr>
            <tr><td><code>stream_options</code></td><td>object</td><td><span class="doc-opt">可选</span></td>
              <td>如 <code>{"include_usage": true}</code>；网关对流式请求会强制注入，无需手动携带。</td></tr>
            <tr><td><code>temperature</code></td><td>number</td><td><span class="doc-opt">可选</span></td>
              <td>采样温度 0–2，默认跟随上游。</td></tr>
            <tr><td><code>top_p</code></td><td>number</td><td><span class="doc-opt">可选</span></td>
              <td>核采样概率，默认跟随上游。</td></tr>
            <tr><td><code>max_tokens</code></td><td>integer</td><td><span class="doc-opt">可选</span></td>
              <td>本次回复的最大生成 token 数。</td></tr>
            <tr><td><code>stop</code></td><td>string / array</td><td><span class="doc-opt">可选</span></td>
              <td>最多 4 个停止序列，命中即截断。</td></tr>
            <tr><td><code>seed</code></td><td>integer</td><td><span class="doc-opt">可选</span></td>
              <td>随机种子，尽量（不保证）可复现输出。</td></tr>
            <tr><td><code>presence_penalty</code> / <code>frequency_penalty</code></td><td>number</td><td><span class="doc-opt">可选</span></td>
              <td>存在 / 频率惩罚，-2 到 2。</td></tr>
            <tr><td><code>response_format</code></td><td>object</td><td><span class="doc-opt">可选</span></td>
              <td>如 <code>{"type": "json_object"}</code> 开启 JSON 输出，取决于上游模型支持。</td></tr>
            <tr><td><code>tools</code></td><td>array</td><td><span class="doc-opt">可选</span></td>
              <td>function calling 工具定义，原样透传。</td></tr>
            <tr><td><code>tool_choice</code></td><td>string / object</td><td><span class="doc-opt">可选</span></td>
              <td>工具选择策略（<code>none</code> / <code>auto</code> / 指定函数），原样透传。</td></tr>
            <tr><td><code>user</code></td><td>string</td><td><span class="doc-opt">可选</span></td>
              <td>调用方自定义标识，透传给上游。</td></tr>
          </tbody>
        </table>
      </div>

      <p><code>messages</code> 元素结构：</p>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th style="width: 120px">字段</th><th style="width: 90px">类型</th><th>说明</th></tr>
          </thead>
          <tbody>
            <tr><td><code>role</code></td><td>string</td>
              <td><code>system</code>（设定行为）/ <code>user</code> / <code>assistant</code>（历史回复）/ <code>tool</code>（工具结果）。</td></tr>
            <tr><td><code>content</code></td><td>string / array</td>
              <td>消息内容；多模态模型支持数组形式（文本 + 图片等）。</td></tr>
          </tbody>
        </table>
      </div>

      <div class="doc-callout info">
        <div class="doc-callout-title">透传原则</div>
        <p>除鉴权与计量必需字段外，请求体原样透传所选上游；个别参数（如
          <code>response_format</code>、<code>tools</code>）的实际支持程度取决于所选模型与厂商。
        未列出的字段也会透传，行为以上游为准。</p>
      </div>

      <h2>请求示例</h2>
      <div class="doc-tabs">
        <button v-for="(t, k) in { curl: 'cURL', stream: 'cURL 流式', python: 'Python', node: 'Node.js' }"
          :key="k" class="doc-tab" :class="{ on: exTab === k }" type="button" @click="exTab = k">
          {{ t }}
        </button>
      </div>
      <CodeBlock :lang="examples[exTab].lang" :code="examples[exTab].code" />

      <h2>返回响应</h2>

      <div class="doc-status"><span class="doc-dot ok"></span><span>200 成功</span><span class="code">· 非流式</span></div>
      <p><code>application/json</code>，结构与 OpenAI 一致，<code>usage</code> 为计费依据：</p>
      <CodeBlock lang="json" code='{
  "id": "chatcmpl-8f3a2b1c",
  "object": "chat.completion",
  "created": 1731000000,
  "model": "deepseek-chat",
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "你好！有什么可以帮你？"},
      "finish_reason": "stop"
    }
  ],
  "usage": {"prompt_tokens": 5, "completion_tokens": 9, "total_tokens": 14}
}' />

      <div class="doc-status"><span class="doc-dot ok"></span><span>200 成功</span><span class="code">· 流式（stream=true）</span></div>
      <p><code>text/event-stream</code>，逐块输出 <code>delta</code>；末块（<code>[DONE]</code> 前）携带完整
        <code>usage</code> 供计费：</p>
      <CodeBlock lang="text" code='data: {"id":"chatcmpl-8f3a2b1c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"你"},"finish_reason":null}]}

data: {"id":"chatcmpl-8f3a2b1c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"好"},"finish_reason":null}]}

data: {"id":"chatcmpl-8f3a2b1c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}

data: [DONE]' />

      <div class="doc-status"><span class="doc-dot warn"></span><span>401 / 403 / 429 错误</span></div>
      <p>统一错误格式，全部错误码见<a href="#/docs/api-auth">鉴权与错误码</a>：</p>
      <CodeBlock lang="json" code='{
  "error": {
    "message": "rate limit exceeded, please retry later",
    "type": "rate_limit_error"
  }
}' />

      <div class="doc-callout info">
        <div class="doc-callout-title">计费说明</div>
        <p>响应完成后按 <code>usage</code> 结算：<code>ceil((输入tokens × 输入单价 + 输出tokens × 输出单价) / 1M)</code>，
          子账号与客户额度同事务双记账；上游未返回 usage 的请求不计量。命中精确缓存时成本为 0
          （响应头 <code>X-Tg-Cache: hit</code>）。</p>
      </div>
    </div>
  </article>
</template>
