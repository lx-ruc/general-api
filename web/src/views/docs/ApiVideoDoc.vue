<script setup lang="ts">
import { computed, ref } from 'vue'
import CodeBlock from './CodeBlock.vue'

// 视频生成接口文档（Ark 原生任务式透传）：创建任务 + 轮询查询 + 计费口径
const baseURL = `${location.origin}/v1`

const exTab = ref('curl')
const examples = computed<Record<string, { lang: string; code: string }>>(() => ({
  curl: {
    lang: 'bash',
    code: `# 1) 创建任务：文生视频（参数写在 text 尾部）
curl ${baseURL}/videos/generations/tasks \\
  -H "Authorization: Bearer {你的密钥}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "doubao-seedance-1",
    "content": [
      {"type": "text", "text": "戴着帽子的老爷爷面带微笑往前走 --ratio 16:9 --dur 5"}
    ]
  }'
# → {"id": "cgt-20260101-abcdef"}

# 2) 轮询查询：拿到 video_url 即生成完成（几秒到几分钟）
curl ${baseURL}/videos/generations/tasks/cgt-20260101-abcdef \\
  -H "Authorization: Bearer {你的密钥}"`,
  },
  image: {
    lang: 'bash',
    code: `# 图生视频：content 数组带图（首帧参考）+ 文本指令
curl ${baseURL}/videos/generations/tasks \\
  -H "Authorization: Bearer {你的密钥}" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "doubao-seedance-1",
    "content": [
      {"type": "image_url", "image_url": {"url": "https://example.com/first-frame.png"}},
      {"type": "text", "text": "让画面中的人物自然挥手 --dur 5"}
    ]
  }'`,
  },
  python: {
    lang: 'python',
    code: `import time
import requests

BASE = "${baseURL}"
KEY = "{你的密钥}"
HEADERS = {"Authorization": f"Bearer {KEY}", "Content-Type": "application/json"}

# 创建任务
task = requests.post(f"{BASE}/videos/generations/tasks", headers=HEADERS, json={
    "model": "doubao-seedance-1",
    "content": [{"type": "text", "text": "小猫追蝴蝶 --ratio 1:1 --dur 5"}],
}).json()
task_id = task["id"]

# 轮询到终态（queued → running → succeeded / failed）
while True:
    task = requests.get(f"{BASE}/videos/generations/tasks/{task_id}",
                        headers=HEADERS).json()
    if task["status"] in ("succeeded", "failed"):
        break
    time.sleep(5)

print(task["content"]["video_url"])   # 成果视频下载地址
print(task["usage"]["completion_tokens"])  # 本次计费 token 数`,
  },
}))
</script>

<template>
  <article class="doc-article">
    <div class="doc-kicker">API 参考</div>
    <h1 class="doc-h1">视频生成</h1>
    <p class="doc-desc">视频生成模型（Seedance 等）走异步任务式接口：先创建任务拿到任务 id，再轮询查询；
      请求体与火山方舟原生协议完全一致，从官方代码迁移只需替换域名与密钥。</p>

    <div class="doc-endpoint">
      <span class="doc-method post">POST</span>
      <span class="doc-path">/v1/videos/generations/tasks</span>
      <span class="doc-base">{{ baseURL }}</span>
    </div>
    <p class="doc-desc" style="margin-top: 4px">创建视频生成任务，立即返回任务 id。</p>

    <div class="doc-endpoint" style="margin-top: 8px">
      <span class="doc-method get">GET</span>
      <span class="doc-path">/v1/videos/generations/tasks/&#123;task_id&#125;</span>
      <span class="doc-base">{{ baseURL }}</span>
    </div>
    <p class="doc-desc" style="margin-top: 4px">查询任务状态与结果；任务归属创建时的密钥所属客户，跨客户查询返回 404。</p>

    <div class="doc-prose">
      <h2>鉴权</h2>
      <p>HTTP Header 携带 <code>Authorization: Bearer {你的密钥}</code>。模型必须在密钥的授权列表内且为
        视频类型模型（在 <code>GET /v1/models</code> 中标注为视频），否则返回 <code>400</code>。</p>

      <h2>创建任务 Body 参数（application/json）</h2>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th style="width: 150px">参数</th><th style="width: 90px">类型</th><th style="width: 70px">必填</th><th>说明</th></tr>
          </thead>
          <tbody>
            <tr><td><code>model</code></td><td>string</td><td><span class="doc-req">必填</span></td>
              <td>视频模型名，如 <code>doubao-seedance-1</code>。</td></tr>
            <tr><td><code>content</code></td><td>array</td><td><span class="doc-req">必填</span></td>
              <td>内容数组：文本指令（<code>{"type":"text","text":"..."}</code>）与参考图
                （<code>{"type":"image_url","image_url":{"url":"..."}}</code>，图生视频用）。元素结构见下表。</td></tr>
          </tbody>
        </table>
      </div>

      <p>生成参数（画面比例、时长、分辨率等）以 <code>--key value</code> 形式写在 text 文本尾部，常用项：</p>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th style="width: 140px">参数</th><th style="width: 110px">示例</th><th>说明</th></tr>
          </thead>
          <tbody>
            <tr><td><code>--ratio</code></td><td><code>--ratio 16:9</code></td><td>画面比例（1:1 / 16:9 / 9:16 / 4:3 / 3:4 / 21:9）。</td></tr>
            <tr><td><code>--dur</code></td><td><code>--dur 5</code></td><td>视频时长（秒），可选范围以模型说明为准。</td></tr>
            <tr><td><code>--resolution</code></td><td><code>--resolution 1080p</code></td><td>输出分辨率（720p / 1080p），部分模型支持。</td></tr>
            <tr><td><code>--fps</code></td><td><code>--fps 24</code></td><td>帧率，部分模型支持。</td></tr>
          </tbody>
        </table>
      </div>
      <p>未列出的参数同样透传上游，实际支持以模型与厂商为准。</p>

      <h2>查询响应</h2>
      <p><code>status</code> 四态：<code>queued</code>（排队）→ <code>running</code>（生成中）→
        <code>succeeded</code> / <code>failed</code>（终态）。成功响应示例：</p>
      <CodeBlock lang="json" code='{
  "id": "cgt-20260101-abcdef",
  "status": "succeeded",
  "content": {"video_url": "https://.../video.mp4"},
  "usage": {"completion_tokens": 1228800, "total_tokens": 1228800}
}' />
      <p>失败响应携带 <code>error.message</code>；<code>video_url</code> 有时效性，请及时下载。</p>

      <h2>请求示例</h2>
      <div class="doc-tabs">
        <button v-for="(t, k) in { curl: 'cURL 全流程', image: '图生视频', python: 'Python 轮询' }"
          :key="k" class="doc-tab" :class="{ on: exTab === k }" type="button" @click="exTab = k">
          {{ t }}
        </button>
      </div>
      <CodeBlock :lang="examples[exTab].lang" :code="examples[exTab].code" />

      <div class="doc-callout info">
        <div class="doc-callout-title">计费说明</div>
        <p>创建任务不计量；任务完成（查询到 <code>succeeded</code>）时按
          <code>usage.completion_tokens</code>（视频生成消耗的 token 数，官方计费口径——视频不统计输入
          token）× 模型输出单价结算：<code>ceil(tokens × 单价 / 1M)</code>。任务失败不产生费用。
          若你创建后从不查询，网关会在后台自动查询并结算，不会漏计。</p>
      </div>

      <div class="doc-callout warn">
        <div class="doc-callout-title">任务归属</div>
        <p>任务只能由创建它的客户（同客户下任意密钥）查询；任务 id 对其他客户一律表现为不存在（404）。
          任务在渠道侧的保留有时效（通常 7 天），过期后查询返回 404，请及时取回结果。</p>
      </div>
    </div>
  </article>
</template>
