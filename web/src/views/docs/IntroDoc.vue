<template>
  <article class="doc-article">
    <div class="doc-kicker">开始使用</div>
    <h1 class="doc-h1">产品介绍</h1>
    <p class="doc-desc">
      general api（token 中转站）是自托管的大模型 API 网关与计量计费平台：把多家厂商 API
      统一转换为自己签发的 OpenAI 兼容接口，并在转发之间完成账号管理、模型授权、token 计量与额度控制。
    </p>

    <div class="doc-prose">
      <h2>它解决什么问题</h2>
      <ul>
        <li><strong>统一接入</strong>：业务方只需把 OpenAI SDK 的 <code>base_url</code> 指向中转站，即可在
          DeepSeek、智谱 GLM、通义千问等任意 OpenAI 兼容厂商之间切换，上游变更对业务零感知。</li>
        <li><strong>集中管控</strong>：三级账号 + 模型级授权——谁能用哪个模型、能用多少，全部在管理台配置；
          厂商 API key 集中保管（AES-256-GCM 加密存储），不再散发到每个人手里。</li>
        <li><strong>计量计费</strong>：每次调用记录 token 用量并按模型单价计费，子账号与客户两级额度同步扣减，
          月度对账单、成本中心、额度预警开箱即用。</li>
      </ul>

      <h2>整体架构</h2>
      <p>单端口同服三个平面，前端管理台 embed 进单个 Go 二进制，部署只需一个可执行文件 + 一份配置：</p>
      <div class="doc-table-wrap">
        <table class="doc-table">
          <thead>
            <tr><th>平面</th><th>路径</th><th>鉴权</th><th>作用</th></tr>
          </thead>
          <tbody>
            <tr>
              <td><code>数据面</code></td>
              <td><code>/v1/chat/completions</code>、<code>/v1/models</code></td>
              <td>API key（Bearer）</td>
              <td>OpenAI 兼容端点，供业务方调用，支持 SSE 流式</td>
            </tr>
            <tr>
              <td><code>管理面</code></td>
              <td><code>/api/*</code></td>
              <td>JWT + RBAC</td>
              <td>管理台 REST 接口（客户 / 子账号 / 渠道 / 额度 / 报表）</td>
            </tr>
            <tr>
              <td><code>运维面</code></td>
              <td><code>/healthz</code>、<code>/metrics</code></td>
              <td>免鉴权（metrics 可选 token）</td>
              <td>健康检查与 Prometheus 指标</td>
            </tr>
          </tbody>
        </table>
      </div>

      <h2>三级账号体系</h2>
      <div class="doc-grid-3">
        <div class="doc-role">
          <h4>系统管理员</h4>
          <p>对接厂商：维护渠道（上游地址 + 密钥池 + 模型列表）与模型定价；创建客户、分配额度；
            处理充值审批；查看全局用量与跨客户报表。</p>
        </div>
        <div class="doc-role">
          <h4>客户管理员</h4>
          <p>管理本客户：创建子账号并做模型级授权、分配子账号额度、审批额度申请；
            维护成本中心词表；查看客户对账单与调用日志。</p>
        </div>
        <div class="doc-role">
          <h4>子账号</h4>
          <p>自助接入：在「我的密钥」创建 API key（明文仅显示一次），按个人被授权的模型调用；
            查看自己的用量，额度不够发起申请。</p>
        </div>
      </div>

      <h2>核心特性</h2>
      <ul>
        <li><strong>渠道路由</strong>：同模型多渠道按优先级 + 权重负载均衡，失败自动降级备用渠道；
          单渠道连续失败自动熔断，修复后手动恢复。</li>
        <li><strong>高并发</strong>：渠道多 Key 池轮转、上游 429 自动冷却换 Key、并发闸门排队削峰、
          非流式精确缓存（命中不计费）；多实例可接 Redis 全局协调。</li>
        <li><strong>计量计费</strong>：全整数运算（无浮点误差），流式请求自动注入
          <code>stream_options.include_usage</code> 保证末块带用量。</li>
        <li><strong>额度控制</strong>：客户 / 子账号两级「设上限、双记账」，单月上限次月自动清零，
          客户总额度耗尽自动欠费停服、充值即恢复。</li>
        <li><strong>在线体验</strong>：顶栏对话框选模型直接流式试聊，与 API 调用走完全一致的路由与计费链路。</li>
        <li><strong>安全</strong>：API key 明文不落库（SHA-256 唯一索引）、上游密钥 AES 加密、密码 bcrypt、
          调用日志只记计量元数据不落对话内容。</li>
      </ul>

      <div class="doc-callout info">
        <div class="doc-callout-title">部署形态</div>
        <p>单机：单二进制 + SQLite，开箱即用。对外高并发：PostgreSQL + 双网关 + Caddy（TLS / SSE 透传），
          实测单机 2000+ RPS。详见仓库 <code>deploy/</code> 与 <code>docs/高并发设计.md</code>。</p>
      </div>

      <h2>接下来</h2>
      <div class="doc-cards">
        <router-link class="doc-card" to="/docs/quickstart">
          <div class="t">快速开始</div>
          <div class="d">按角色走通首次配置，5 分钟完成业务接入。</div>
        </router-link>
        <router-link class="doc-card" to="/docs/features">
          <div class="t">核心功能</div>
          <div class="d">渠道路由、计量计费、额度控制、对账单的完整说明。</div>
        </router-link>
        <router-link class="doc-card" to="/docs/api-chat">
          <div class="t">API 参考</div>
          <div class="d">对话补全接口的参数、示例与错误码。</div>
        </router-link>
      </div>
    </div>
  </article>
</template>
