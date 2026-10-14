<script setup lang="ts">
// 登录页左侧品牌级流转示意：供应商品牌 → 慧沐引擎 → 企业 → 子账号 四层静态拓扑。
// 纯品牌展示（不出现具体模型版本、不模拟实时数据/请求流），
// 唯一的动画是连线虚线流动——纯装饰非数据，prefers-reduced-motion 关停。

// 供应商品牌色：墨调深色（浅底上 4.5:1+ 可读）
const VENDORS = [
  { name: '火山方舟', hue: '#1f5fd6' },
  { name: 'DeepSeek', hue: '#3453c4' },
  { name: '智谱 GLM', hue: '#7449c9' },
  { name: '通义千问', hue: '#b45309' },
  { name: 'Kimi', hue: '#0e7d94' },
]

// 右侧签发层级：网关 → 多个企业（客户）→ 每企业多个子账号
const ORGS = [
  { name: '企业 A', y: 108 },
  { name: '企业 B', y: 180 },
  { name: '企业 C', y: 252 },
]
const SUB_OFFSETS = [-26, 0, 26] // 每企业 3 个子账号的纵向偏移
</script>

<template>
  <aside class="board" aria-hidden="true">
    <!-- 头部：品牌主张（真实产品描述，无虚构数字） -->
    <header class="mod m1 board-head">
      <h2 class="board-title">统一大模型接入网关</h2>
      <p class="board-sub">
        把火山方舟、DeepSeek、智谱 GLM、通义千问、Kimi 等任意 OpenAI 兼容厂商收敛为自己签发的统一端点：
        模型级授权、多渠道路由容错、token 级计量计费与双层额度控制。
      </p>
    </header>

    <!-- 品牌级流转拓扑：供应商 → 网关枢纽 → 企业 → 子账号 -->
    <div class="mod m2 flow-wrap">
      <svg class="flow" viewBox="0 0 760 360" preserveAspectRatio="xMidYMid meet">
        <!-- 供应商 → 网关 的连线 -->
        <path v-for="(v, i) in VENDORS" :key="'e' + i" class="edge"
          :style="{ stroke: v.hue }"
          :d="`M162 ${50 + i * 65} C 236 ${50 + i * 65}, 258 180, 328 180`" />
        <!-- 网关 → 企业 的连线 -->
        <path v-for="(o, i) in ORGS" :key="'oe' + i" class="edge edge-out"
          :d="`M474 ${162 + i * 18} C 510 ${162 + i * 18}, 512 ${o.y}, 546 ${o.y}`" />
        <!-- 企业 → 子账号 的连线 -->
        <template v-for="(o, i) in ORGS" :key="'se' + i">
          <path v-for="(dy, j) in SUB_OFFSETS" :key="j" class="edge edge-sub"
            :d="`M640 ${o.y} C 652 ${o.y}, 656 ${o.y + dy}, 674 ${o.y + dy}`" />
        </template>

        <!-- 供应商品牌节点：品牌名 + 品牌色点（不体现具体模型版本） -->
        <g v-for="(v, i) in VENDORS" :key="'n' + i" class="vnode" :style="{ '--hue': v.hue }">
          <rect class="vnode-box" x="16" :y="50 + i * 65 - 22" width="146" height="44" rx="9" />
          <circle class="vnode-dot" cx="36" :cy="50 + i * 65" r="3.6" :fill="v.hue" />
          <text class="vnode-name" x="48" :y="50 + i * 65 + 4.5">{{ v.name }}</text>
        </g>

        <!-- 网关枢纽：主节点 + 真实能力标签 -->
        <g class="hub">
          <rect x="328" y="140" width="146" height="80" rx="12" />
          <text class="hub-name" x="401" y="168">慧沐引擎</text>
          <text class="hub-sub" x="401" y="188">OpenAI 兼容 · SSE</text>
          <text class="hub-sub" x="401" y="205">计量 · 转发 · 计费</text>
        </g>

        <!-- 企业节点（客户）与子账号：一层签发、多层使用 -->
        <g v-for="(o, i) in ORGS" :key="'on' + i" class="onode">
          <rect x="546" :y="o.y - 17" width="94" height="34" rx="8" />
          <text class="onode-name" x="593" :y="o.y + 4">{{ o.name }}</text>
        </g>
        <g v-for="(o, i) in ORGS" :key="'sg' + i" class="snode">
          <g v-for="(dy, j) in SUB_OFFSETS" :key="j">
            <rect x="674" :y="o.y + dy - 11" width="68" height="22" rx="6" />
            <text x="708" :y="o.y + dy + 3">子账号</text>
          </g>
        </g>
        <!-- 省略号：接入企业不止图中三家 -->
        <g class="more">
          <circle cx="593" cy="306" r="1.3" />
          <circle cx="593" cy="314" r="1.3" />
          <circle cx="593" cy="322" r="1.3" />
        </g>
      </svg>
    </div>
  </aside>
</template>

<style scoped>
.board {
  /* 整屏左半幅：纸面 + 工程纸网格，无边框卡片（与右侧白色登录竖栏以细线相接） */
  --bd-line: var(--tg-line);
  --bd-ink: var(--tg-ink);
  --bd-sub: var(--tg-graphite);
  --bd-muted: var(--tg-muted);
  --bd-green: var(--tg-green);
  --bd-green-ink: var(--tg-green-ink);
  position: relative;
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  display: flex; flex-direction: column;
  background:
    radial-gradient(760px 460px at 16% -12%, rgba(18, 164, 98, 0.06), transparent 62%),
    var(--tg-paper);
  color: var(--bd-ink);
}

.mod { position: relative; animation: mod-rise 0.55s cubic-bezier(0.2, 0.8, 0.3, 1) both; }
.m1 { animation-delay: 0.05s; }
.m2 { animation-delay: 0.16s; }
@keyframes mod-rise {
  from { opacity: 0; transform: translateY(12px); }
  to { opacity: 1; transform: none; }
}

/* ---------- 头部 ---------- */
.board-head { padding: 34px 38px 4px; }
.board-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--bd-ink); letter-spacing: 0.01em; }
.board-sub {
  margin: 12px 0 0; max-width: 560px;
  font-size: 13px; line-height: 1.8; color: var(--bd-sub);
}

/* ---------- 拓扑（吃满剩余高度，居中） ---------- */
.flow-wrap {
  flex: 1 1 auto; min-height: 0;
  display: flex; align-items: center; justify-content: center;
  padding: 10px 38px 26px;
}
.flow { width: 100%; height: 100%; max-width: 820px; display: block; }

/* 唯一保留的动画：虚线缓慢流动（纯装饰，非数据） */
.edge {
  fill: none; stroke-width: 1.2; opacity: 0.55;
  stroke-dasharray: 5 9;
  animation: dashflow 1.05s linear infinite;
}
.edge-out { stroke: var(--tg-green); }
@keyframes dashflow { to { stroke-dashoffset: -14; } }

.vnode-box {
  fill: var(--tg-paper);
  stroke: color-mix(in srgb, var(--hue) 45%, transparent);
  stroke-width: 1;
}
.vnode-dot { opacity: 0.95; }
.vnode-name { font-size: 13px; font-weight: 600; fill: var(--bd-ink); }

/* 网关枢纽 = 右侧登录按钮同款祖母绿实体（左右呼应的锚点） */
.hub rect {
  fill: var(--tg-green);
  filter: drop-shadow(0 4px 12px rgba(18, 164, 98, 0.28));
}
.hub-name { font-size: 15px; font-weight: 700; fill: var(--tg-btn-ink); text-anchor: middle; }
.hub-sub { font-size: 10px; fill: rgba(15, 43, 32, 0.78); text-anchor: middle; }

/* 企业节点：签发给客户企业（绿系浅底，层级介于枢纽与子账号之间） */
.onode rect { fill: var(--tg-green-wash); stroke: var(--tg-green-wash-strong); stroke-width: 1; }
.onode-name { font-size: 12px; font-weight: 600; fill: var(--tg-green-ink); text-anchor: middle; }
/* 子账号：最末层级，弱化成浅色小票，只承载「多个」的阵列感 */
.snode rect { fill: color-mix(in srgb, var(--tg-green-wash) 60%, transparent); }
.snode text { font-size: 9.5px; fill: var(--bd-sub); text-anchor: middle; }
.edge-sub { stroke: var(--tg-green); opacity: 0.55; stroke-dasharray: 3 4; }
.more circle { fill: var(--bd-muted); }

/* ---------- 降级 ---------- */
@media (prefers-reduced-motion: reduce) {
  .mod { animation: none; }
  .edge { animation: none; }
}
</style>
