<script setup lang="ts">
import { computed } from 'vue'
import CodeBlock from './CodeBlock.vue'

// 鉴权与错误码：base_url 跟随当前部署地址
const baseURL = `${location.origin}/v1`

const authExample = computed(() => `curl ${baseURL}/models \\
  -H "Authorization: Bearer sk-你的密钥"`)
</script>

<template>
  <article class="doc-article">
    <div class="doc-kicker">API 参考</div>
    <h1 class="doc-h1">鉴权与错误码</h1>
    <p class="doc-desc">数据面接口使用 API key 鉴权，错误响应遵循 OpenAI 错误格式。</p>

    <div class="doc-prose">
      <h2>Base URL 与鉴权</h2>
      <p>所有数据面接口以 <code>{{ baseURL }}</code> 为前缀，鉴权使用
        <code>Authorization: Bearer &lt;你的密钥&gt;</code> 请求头：</p>
      <CodeBlock :code="authExample" lang="bash" />

      <ul>
        <li>密钥格式为 <code>sk-</code> 前缀，在管理台【我的密钥】创建，<strong>明文仅创建时显示一次</strong>。</li>
        <li>一个密钥可调用多个模型，能调哪些由客户管理员的「模型授权」决定；<code>GET /v1/models</code>
          返回的就是该密钥可用的模型清单。</li>
        <li>每个密钥有独立的每分钟请求数（RPM）限额，默认 60，可由系统管理员调整。</li>
      </ul>

      <h2>OpenAI SDK 兼容</h2>
      <p>任何 OpenAI 兼容客户端只需替换 <code>base_url</code> 与 <code>api_key</code>：</p>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th>语言 / SDK</th><th>配置</th></tr>
          </thead>
          <tbody>
            <tr>
              <td>Python <code>openai</code></td>
              <td><code>OpenAI(api_key="sk-...", base_url="{{ baseURL }}")</code></td>
            </tr>
            <tr>
              <td>Node.js <code>openai</code></td>
              <td><code>new OpenAI({ apiKey: "sk-...", baseURL: "{{ baseURL }}" })</code></td>
            </tr>
            <tr>
              <td>curl / HTTP</td>
              <td>请求 <code>{{ baseURL }}/chat/completions</code>，带 <code>Authorization: Bearer</code> 头</td>
            </tr>
          </tbody>
        </table>
      </div>

      <h2>错误响应格式</h2>
      <p>所有错误返回统一的 OpenAI 格式，<code>error.type</code> 是稳定的机器可读错误码：</p>
      <CodeBlock lang="json" code='{
  "error": {
    "message": "you are not allowed to use model \"gpt-4\", please contact your company admin",
    "type": "model_not_allowed"
  }
}' />

      <h2>错误码一览</h2>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th>HTTP</th><th>type</th><th>含义与处理建议</th></tr>
          </thead>
          <tbody>
            <tr><td><code>401</code></td><td><code>invalid_api_key</code></td>
              <td>密钥缺失、格式错误、不存在 / 已禁用或已过期。检查 Bearer 头与密钥全文。</td></tr>
            <tr><td><code>400</code></td><td><code>invalid_request_error</code></td>
              <td>请求体不是合法 JSON，或缺少必填的 <code>model</code> 参数。</td></tr>
            <tr><td><code>404</code></td><td><code>invalid_request_error</code></td>
              <td>模型不存在或未启用。以 <code>GET /v1/models</code> 返回的模型名为准。</td></tr>
            <tr><td><code>403</code></td><td><code>model_not_allowed</code></td>
              <td>当前密钥未被授权使用该模型，联系客户管理员在「模型授权」中开通。</td></tr>
            <tr><td><code>403</code></td><td><code>insufficient_balance</code></td>
              <td>客户欠费停服（总额度耗尽自动置位）。联系系统管理员充值，到账自动恢复。</td></tr>
            <tr><td><code>429</code></td><td><code>insufficient_balance</code></td>
              <td>子账号或客户额度耗尽。子账号可在管理台发起「额度申请」。</td></tr>
            <tr><td><code>429</code></td><td><code>monthly_limit_exceeded</code></td>
              <td>当月消费达到单月上限，次月自动恢复。</td></tr>
            <tr><td><code>429</code></td><td><code>rate_limit_error</code></td>
              <td>密钥每分钟请求数超限（默认 60 RPM）。按指数退避重试，或联系管理员调高。</td></tr>
            <tr><td><code>429</code></td><td><code>upstream_busy</code></td>
              <td>上游全部限流中，网关已自动换 Key / 换渠道重试后仍失败。带 <code>Retry-After</code> 头，稍后重试。</td></tr>
            <tr><td><code>502</code></td><td><code>upstream_error</code></td>
              <td>上游异常（5xx / 网络错误），已尝试全部候选渠道。可重试；持续出现请查看平台渠道状态。</td></tr>
          </tbody>
        </table>
      </div>

      <div class="doc-callout warn">
        <div class="doc-callout-title">重试建议</div>
        <p>对 <code>429</code> 类错误做指数退避重试（OpenAI SDK 默认自带）；<code>401 / 403</code> 属于配置问题，
          重试无意义；<code>400 / 404</code> 请先修正请求。</p>
      </div>

      <h2>诊断响应头</h2>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th>响应头</th><th>含义</th></tr>
          </thead>
          <tbody>
            <tr><td><code>X-Tg-Channel-Id</code></td><td>实际服务本次请求的渠道 ID（平台侧定位上游问题用）。</td></tr>
            <tr><td><code>X-Tg-Cache: hit</code></td><td>精确缓存命中，本次请求未访问上游、不计费。</td></tr>
            <tr><td><code>Retry-After</code></td><td><code>429 upstream_busy</code> 时建议的等待秒数。</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </article>
</template>
