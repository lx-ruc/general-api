<template>
  <article class="doc-article">
    <div class="doc-kicker">功能指南</div>
    <h1 class="doc-h1">核心功能</h1>
    <p class="doc-desc">渠道路由、计量计费、额度控制、预警与对账的完整语义说明。</p>

    <div class="doc-prose">
      <h2>渠道与模型路由</h2>
      <p>一条「渠道」= 一家上游厂商的一份配置（base_url + 密钥池 + 模型列表）。请求按
        <code>model</code> 参数找到所有支持该模型的启用渠道，然后：</p>
      <ul>
        <li><strong>优先级 + 权重负载均衡</strong>：最高优先级组内按权重加权随机分流，其余组按优先级顺序作为降级备用。</li>
        <li><strong>多 Key 池</strong>：一个渠道可配多把上游密钥（每行一把，可带 <code>:权重</code>），
          突破单 Key 的 RPM/TPM 限制；上游 429 自动冷却该 Key 并换下一把。</li>
        <li><strong>熔断</strong>：单渠道连续失败达到阈值（默认 5 次）自动禁用并写备注，需在管理台手动恢复。</li>
        <li><strong>并发闸门</strong>：可对单渠道设最大并发，超出的请求有界排队等待（削峰），而非直接打爆上游。</li>
        <li><strong>透明可观测</strong>：响应头 <code>X-Tg-Channel-Id</code> 标识实际服务渠道；
          全部候选耗尽时，若仅剩限流类失败返回 <code>429 upstream_busy</code>（带 <code>Retry-After</code>），否则 <code>502</code>。</li>
      </ul>

      <h2>计量计费</h2>
      <ul>
        <li><strong>计费公式</strong>：<code>单次成本 = ceil((输入tokens × 输入单价 + 输出tokens × 输出单价) / 1M)</code>，
          全整数运算无浮点误差。单价以「点数 / M token」存储，默认 <code>1 元 = 1,000,000 点</code>。</li>
        <li><strong>双层记账</strong>：每次调用在同一个结算事务里累加子账号 <code>quota_used</code> 与客户
          <code>quota_used</code>，两级都是硬上限；结算在响应发出后无条件执行，超扣幅度封顶在单请求成本内。</li>
        <li><strong>流式保证计费</strong>：流式请求自动注入 <code>stream_options.include_usage</code>，
          保证末块携带用量；上游未返回用量的请求标记不计量（不误扣）。</li>
        <li><strong>缓存命中不计费</strong>：开启精确缓存后，相同请求命中缓存直接返回
          （响应头 <code>X-Tg-Cache: hit</code>），记录用量日志但成本为 0。</li>
        <li><strong>额度流水</strong>：额度上限的一切变更都与 QuotaGrant 流水同事务，恒满足
          Σ流水 == 当前上限，可完整审计每一分的来龙去脉。</li>
      </ul>

      <h2>额度与月度上限</h2>
      <ul>
        <li><strong>设上限式分配</strong>：不存在点数划拨——管理员只设 <code>quota_limit</code>，
          消耗只有 <code>quota_used</code> 一个真相来源，追加额度即抬高上限。</li>
        <li><strong>单月上限</strong>：客户 / 子账号可另设当月消费上限（0 = 不限），达到即拦截
          （<code>429 monthly_limit_exceeded</code>），次月首笔结算自动清零恢复。</li>
        <li><strong>欠费停服</strong>：客户总额度耗尽自动转「欠费停服」，数据面请求被拦
          （<code>403 insufficient_balance</code>）但管理台仍可登录；平台追加额度后自动恢复，
          与手动停用互不干扰。</li>
      </ul>

      <h2>额度预警与月度对账单</h2>
      <ul>
        <li><strong>两级水位告警</strong>：客户 / 子账号额度消耗越过阈值（默认 80%，可按客户设置）邮件提醒，
          每档只提醒一次、追加额度后自动复位；客户额度 100% 耗尽同时通知系统管理员跟进续费。</li>
        <li><strong>三段式月账单</strong>：勾稽（期末 − 期初 == 期内消耗，自动校验）/ 冲减（负数授权即冲减回收入）/
          明细（模型 × 成本中心 × 日聚合），月末自动快照、漏跑自愈，可手动补跑；CSV 带 BOM 可直接双击打开。</li>
      </ul>

      <h2>成本中心</h2>
      <p>客户维护受控中心词表（如「AI 客服」「数据分析」），API key 挂靠中心；结算时把中心快照写进调用日志——
        改派只影响未来，历史账单不可变。客户报表按中心聚合，未归集的用量单独披露占比；
        平台可看跨客户的中心毛利交叉报表。</p>

      <h2>在线体验</h2>
      <p>顶栏「在线体验」对话框：选模型直接流式试聊。它注入合成身份复用 <code>/v1</code> 数据面的完整编排
        （密钥池 / 排队 / 熔断 / SSE / 计费全一致）：子账号按个人授权、客户管理员按客户授权并集，
        消耗真实额度；对话内容不落审计与日志，计量记录以 KeyID=0 标识。</p>

      <h2>高可用与运维</h2>
      <ul>
        <li><strong>双模式数据库</strong>：SQLite 单机开箱即用；PostgreSQL 模式支持多实例水平扩容（含存量数据迁移工具）。</li>
        <li><strong>全局协调</strong>：多实例接入 Redis 后，Key 冷却 / 并发闸门 / 缓存全局共享；
          Redis 故障自动降级为单机内存模式，不拖死数据面。</li>
        <li><strong>监控</strong>：<code>GET /metrics</code> 输出 Prometheus 指标（请求量、延迟直方图、活跃流、
          上游错误、熔断、Key 冷却、排队时长、缓存命中等）。</li>
      </ul>

      <h2>安全</h2>
      <ul>
        <li>用户 API key 明文不落库不写日志：SHA-256 哈希 + 唯一索引 O(1) 查找，明文仅创建时显示一次。</li>
        <li>上游厂商密钥 AES-256-GCM 加密存储，接口永不回显（更新时留空 = 不修改）。</li>
        <li>三层越权防护：JWT 角色守卫 + 客户隔离强制 WHERE + 对象归属校验；登录限速 5 次 / 分 / IP。</li>
        <li>调用日志只记计量元数据（模型、token、成本、渠道），不落对话内容。</li>
      </ul>
    </div>
  </article>
</template>
