<script setup lang="ts">
import { computed } from 'vue'
import CodeBlock from './CodeBlock.vue'

// 快速开始：按角色走通首次配置；接入示例的 base_url 跟随当前部署地址
const baseURL = `${location.origin}/v1`

const curlExample = computed(() => `curl ${baseURL}/chat/completions \\
  -H "Authorization: Bearer sk-你的密钥" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "deepseek-chat",
    "messages": [{"role": "user", "content": "你好"}]
  }'`)

const pythonExample = computed(() => `from openai import OpenAI

client = OpenAI(
    api_key="sk-你的密钥",
    base_url="${baseURL}",
)

resp = client.chat.completions.create(
    model="deepseek-chat",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)`)
</script>

<template>
  <article class="doc-article">
    <div class="doc-kicker">开始使用</div>
    <h1 class="doc-h1">快速开始</h1>
    <p class="doc-desc">按你的角色找到对应路径：系统管理员部署与对接厂商，客户管理员组织子账号与授权，子账号接入 API。</p>

    <div class="doc-prose">
      <h2>系统管理员：让平台可用</h2>
      <ol class="doc-steps">
        <li>
          <span class="st">部署并启动</span>
          <span class="sd">单二进制部署：构建产物 + <code>config.yaml</code> 即可运行（详见仓库 README）。
            首次启动自动建表、写入预置渠道（DeepSeek / 智谱 / 通义，单价 0）并创建系统管理员账号。</span>
        </li>
        <li>
          <span class="st">配置渠道密钥</span>
          <span class="sd">【渠道管理】给预置渠道填入厂商 API Key 并启用，点「测试」验证连通。
            高并发场景可在密钥框每行填一把 Key 组成密钥池，自动轮转调度。</span>
        </li>
        <li>
          <span class="st">模型定价</span>
          <span class="sd">【模型定价】按厂商价目设置输入 / 输出单价（点数 / 百万 token，
            默认 1 元 = 1,000,000 点，即 ¥2/M 的模型填 2,000,000）。</span>
        </li>
        <li>
          <span class="st">开通客户</span>
          <span class="sd">【客户管理】新建客户（含首任客户管理员）并分配总额度；
            客户也可通过注册页自助注册，由平台审批额度后开通。</span>
        </li>
      </ol>

      <h2>客户管理员：组织子账号与授权</h2>
      <ol class="doc-steps">
        <li>
          <span class="st">创建子账号并授权模型</span>
          <span class="sd">【子账号管理】新建子账号账号，勾选「模型授权」——只有被授权的模型才会出现在子账号的
            <code>/v1/models</code> 列表里，未授权模型调用会被拒绝。</span>
        </li>
        <li>
          <span class="st">分配子账号额度</span>
          <span class="sd">建号时设初始额度；后续子账号在「额度申请」发起申请，你一键批准自动追加。
            子账号额度与客户总额度双层硬上限，同事务双记账。</span>
        </li>
        <li>
          <span class="st">（可选）成本中心</span>
          <span class="sd">【成本中心】维护中心词表（如「AI 客服」「数据分析」），把子账号密钥挂靠到中心；
            调用日志与对账单按中心聚合，改派只影响未来、历史账单不可变。</span>
        </li>
        <li>
          <span class="st">日常运营</span>
          <span class="sd">【客户看板】看用量趋势与额度水位；月末在【对账单】下载三段式月账单
            （勾稽 / 冲减 / 明细）。</span>
        </li>
      </ol>

      <h2>子账号：5 分钟接入 API</h2>
      <ol class="doc-steps">
        <li>
          <span class="st">创建 API key</span>
          <span class="sd">登录管理台 →【我的密钥】→ 新建。明文密钥 <code>sk-</code> 开头，
            <strong>仅创建时显示一次</strong>，请立即保存到安全的地方。</span>
        </li>
        <li>
          <span class="st">确认可用模型</span>
          <span class="sd">【可用模型】列出你被授权且已启用的模型及其单价；不确定模型名时以这里为准，
            也可直接调用 <code>GET /v1/models</code>。</span>
        </li>
        <li>
          <span class="st">发起第一次调用</span>
          <span class="sd">把任意 OpenAI 兼容客户端的 <code>base_url</code> 指向本站
            <code>{{ baseURL }}</code>，密钥放入 <code>Authorization: Bearer</code> 头即可。</span>
        </li>
      </ol>

      <CodeBlock :code="curlExample" lang="bash" />
      <CodeBlock :code="pythonExample" lang="python" />

      <div class="doc-callout info">
        <div class="doc-callout-title">在线体验</div>
        <p>不想写代码先试试效果？点顶栏「在线体验」，选模型直接流式对话——
          它与 API 调用走完全一致的路由与计费链路，消耗真实额度。</p>
      </div>

      <h2>接入检查清单</h2>
      <ul>
        <li>返回 <code>401 invalid_api_key</code> → 密钥填错或未带 <code>Bearer </code> 前缀。</li>
        <li>返回 <code>403 model_not_allowed</code> → 该模型未授权给你，联系客户管理员。</li>
        <li>返回 <code>404</code> → 模型名写错，以「可用模型」列表为准。</li>
        <li>返回 <code>429 rate_limit_error</code> → 密钥触发每分钟请求数上限（默认 60），稍后重试或联系管理员调高。</li>
        <li>流式不生效 → 客户端要按 SSE 处理响应（curl 加 <code>-N</code>，SDK 传 <code>stream=True</code>）。</li>
      </ul>
    </div>
  </article>
</template>
