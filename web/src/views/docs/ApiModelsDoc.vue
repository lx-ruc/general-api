<script setup lang="ts">
import { computed } from 'vue'
import CodeBlock from './CodeBlock.vue'

// 模型列表接口文档（apifox 风格）
const baseURL = `${location.origin}/v1`

const curlExample = computed(() => `curl ${baseURL}/models \\
  -H "Authorization: Bearer sk-你的密钥"`)
</script>

<template>
  <article class="doc-article">
    <div class="doc-kicker">API 参考</div>
    <h1 class="doc-h1">模型列表</h1>
    <p class="doc-desc">列出当前密钥可用的模型。OpenAI Models 协议兼容。</p>

    <div class="doc-endpoint">
      <span class="doc-method get">GET</span>
      <span class="doc-path">/v1/models</span>
      <span class="doc-base">{{ baseURL }}</span>
    </div>

    <div class="doc-prose">
      <h2>鉴权</h2>
      <p>HTTP Header 携带 <code>Authorization: Bearer sk-你的密钥</code>。</p>

      <h2>说明</h2>
      <ul>
        <li>返回<strong>该密钥所属子账号已获授权且已启用</strong>的模型——不是平台全部模型；
          申请新模型请联系客户管理员。</li>
        <li>除 OpenAI 标准字段外，额外返回 <code>display_name</code>（展示名）。</li>
        <li>模型单价在管理台【可用模型】查看，计费公式见<a href="#/docs/features">核心功能</a>。</li>
      </ul>

      <h2>请求示例</h2>
      <CodeBlock lang="bash" :code="curlExample" />

      <h2>返回响应</h2>

      <div class="doc-status"><span class="doc-dot ok"></span><span>200 成功</span></div>
      <CodeBlock lang="json" code='{
  "object": "list",
  "data": [
    {
      "id": "deepseek-chat",
      "object": "model",
      "owned_by": "deepseek",
      "display_name": "DeepSeek Chat"
    },
    {
      "id": "glm-4-flash",
      "object": "model",
      "owned_by": "zhipu",
      "display_name": "GLM-4-Flash"
    }
  ]
}' />

      <div class="doc-status"><span class="doc-dot warn"></span><span>401 未授权</span></div>
      <CodeBlock lang="json" code='{
  "error": {"message": "invalid API key", "type": "invalid_api_key"}
}' />

      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th style="width: 150px">字段</th><th>说明</th></tr>
          </thead>
          <tbody>
            <tr><td><code>id</code></td><td>模型名，调用 <code>/v1/chat/completions</code> 时填入 <code>model</code> 字段。</td></tr>
            <tr><td><code>object</code></td><td>固定为 <code>"model"</code>。</td></tr>
            <tr><td><code>owned_by</code></td><td>所属厂商 / 渠道商标识。</td></tr>
            <tr><td><code>display_name</code></td><td>展示名（本站扩展字段）。</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </article>
</template>
