<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

// 登录页左侧「网关调度台」看板：厂商 → 网关 → 企业 → 子账号 的模型流动画 + 实时请求流
// 数据为演示用途（首屏营销装置），不请求后端

interface Vendor {
  name: string
  hue: string
  models: string[]      // 节点下展示的短名
  full: string[]        // ticker 用的完整模型名
  tps: number           // 边上吞吐标签的基准值（运行时围绕它缓慢起伏）
}

interface Row {
  id: number
  time: string
  model: string
  hue: string
  tokens: number
  latency: number
  cost: number
  retried: boolean
}

// 厂商数据色：墨调深色（浅底上 4.5:1+ 可读），与「计量台」亮色主题同族
const VENDORS: Vendor[] = [
  { name: 'DeepSeek', hue: '#3453c4', models: ['v4-flash · v4-pro'], full: ['deepseek-v4-flash', 'deepseek-v4-pro'], tps: 0 },
  { name: '智谱 GLM', hue: '#7449c9', models: ['5.3 · 5.3-flash'], full: ['glm-5.3', 'glm-5.3-flash'], tps: 0 },
  { name: '通义千问', hue: '#b45309', models: ['3.8-max · 3.8-flash'], full: ['qwen3.8-max', 'qwen3.8-flash'], tps: 0 },
  { name: 'Kimi', hue: '#0e7d94', models: ['k3 · k2.7-code'], full: ['kimi-k3', 'kimi-k2.7-code'], tps: 0 },
]
// 每家一条基准吞吐，展示值围绕基准缓慢起伏（±15% 正弦 + 微噪声）：
// 静止的速率读数会和 LIVE 徽标打架，但每秒乱跳又会抖成噪声
VENDORS.forEach(v => { v.tps = 140 + Math.floor(Math.random() * 180) })
const tps = ref(VENDORS.map(v => v.tps))

const ALL_MODELS = VENDORS.flatMap(v => v.full.map(m => ({ model: m, hue: v.hue })))

// 右侧签发层级：中转站 → 多个企业（客户）→ 每企业多个子账号
const ORGS = [
  { name: '企业 A', y: 82 },
  { name: '企业 B', y: 170 },
  { name: '企业 C', y: 258 },
]
const SUB_OFFSETS = [-30, 0, 30] // 每企业 3 个子账号的纵向偏移

// ---- 中转计数器 ----
const relayed = ref(286_417_902)
const perSec = 1_842
const relayedText = computed(() => relayed.value.toLocaleString('en-US'))

// ---- 时钟 ----
const clock = ref('')
function fmt(n: number) { return String(n).padStart(2, '0') }
function nowStr() {
  const d = new Date()
  return `${fmt(d.getHours())}:${fmt(d.getMinutes())}:${fmt(d.getSeconds())}`
}

// ---- 请求流 ticker ----
let rowId = 0
function makeRow(): Row {
  const pick = ALL_MODELS[Math.floor(Math.random() * ALL_MODELS.length)]
  const tokens = 60 + Math.floor(Math.random() * 2400)
  return {
    id: ++rowId,
    time: nowStr(),
    model: pick.model,
    hue: pick.hue,
    tokens,
    latency: 90 + Math.floor(Math.random() * 640),
    cost: Math.ceil(tokens * (0.5 + Math.random() * 3)), // 扣减额度（点 = token 预算）
    retried: Math.random() < 0.12, // 少量行展示 429 换渠道自愈
  }
}
const rows = ref<Row[]>([makeRow(), makeRow(), makeRow(), makeRow()])

let tick: ReturnType<typeof setInterval> | undefined
let feed: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  clock.value = nowStr()
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
  tick = setInterval(() => {
    clock.value = nowStr()
    relayed.value += Math.round(perSec * (0.7 + Math.random() * 0.6))
    tps.value = VENDORS.map((v, i) =>
      Math.round(v.tps * (0.85 + 0.15 * Math.sin(Date.now() / 5400 + i * 1.9) + Math.random() * 0.05)))
  }, 1000)
  feed = setInterval(() => {
    rows.value = [makeRow(), ...rows.value].slice(0, 5)
  }, 2100)
})
onBeforeUnmount(() => { clearInterval(tick); clearInterval(feed) })
</script>

<template>
  <aside class="board" aria-hidden="true">
    <!-- 头部：LIVE 徽标 + 中转计数 -->
    <header class="mod m1 board-head">
      <div class="head-left">
        <span class="live"><i class="live-dot"></i>LIVE</span>
        <h2 class="board-title">模型网关 · 实时调度看板</h2>
      </div>
      <div class="head-right">
        <span class="clock num">{{ clock || '--:--:--' }}</span>
      </div>
      <p class="relay">
        <span class="num relay-num">{{ relayedText }}</span>
        <span class="relay-unit">tokens 已中转</span>
        <span class="num relay-rate">{{ perSec.toLocaleString('en-US') }}/s</span>
      </p>
      <div class="stats">
        <span class="stat">已接入 <b class="num">36</b> 家企业</span>
        <span class="stat"><b class="num">214</b> 个子账号</span>
        <span class="stat">可用率 <b class="num">99.95%</b></span>
      </div>
      <p class="board-sub">
        把 DeepSeek、智谱 GLM、通义千问、Kimi 等任意 OpenAI 兼容厂商收敛为自己签发的统一端点：
        模型级授权、多渠道路由容错、token 级计量计费与双层额度控制。
      </p>
    </header>

    <!-- 调度总览：厂商 → 网关枢纽 → 企业 → 子账号 -->
    <div class="mod m2 flow-wrap">
      <svg class="flow" viewBox="0 0 760 340" preserveAspectRatio="xMidYMid meet">
        <!-- 厂商 → 网关 的连线 -->
        <path v-for="(v, i) in VENDORS" :key="'e' + i" class="edge"
          :style="{ stroke: v.hue }"
          :d="`M168 ${56 + i * 82} C 246 ${56 + i * 82}, 262 170, 328 170`" />
        <!-- 网关 → 企业 的连线 -->
        <path v-for="(o, i) in ORGS" :key="'oe' + i" class="edge edge-out"
          :d="`M472 ${148 + i * 22} C 508 ${148 + i * 22}, 512 ${o.y}, 546 ${o.y}`" />

        <!-- 企业 → 子账号 的连线 -->
        <template v-for="(o, i) in ORGS" :key="'se' + i">
          <path v-for="(dy, j) in SUB_OFFSETS" :key="j" class="edge edge-sub"
            :d="`M638 ${o.y} C 650 ${o.y}, 654 ${o.y + dy}, 672 ${o.y + dy}`" />
        </template>

        <!-- 连线上的吞吐标签 -->
        <text v-for="(v, i) in VENDORS" :key="'t' + i" class="edge-label num"
          :fill="v.hue" x="196" :y="50 + i * 82">{{ tps[i] }} token/s</text>

        <!-- 流动数据包：厂商 → 网关 -->
        <template v-for="(v, i) in VENDORS" :key="'p' + i">
          <circle v-for="k in 2" :key="k" class="pkt" :fill="v.hue" r="2.6">
            <animateMotion :dur="2.4 + i * 0.3 + 's'" :begin="(k - 1) * 1.2 + 's'"
              repeatCount="indefinite"
              :path="`M168 ${56 + i * 82} C 246 ${56 + i * 82}, 262 170, 328 170`" />
          </circle>
        </template>
        <!-- 流动数据包：网关 → 企业 -->
        <circle v-for="(o, i) in ORGS" :key="'op' + i" class="pkt pkt-out" r="2.6" fill="#12a462">
          <animateMotion :dur="1.9 + i * 0.4 + 's'" :begin="i * 0.95 + 's'" repeatCount="indefinite"
            :path="`M472 ${148 + i * 22} C 508 ${148 + i * 22}, 512 ${o.y}, 546 ${o.y}`" />
        </circle>

        <!-- 厂商节点 -->
        <g v-for="(v, i) in VENDORS" :key="'n' + i" class="vnode" :style="{ '--hue': v.hue }">
          <rect class="vnode-box" x="16" :y="56 + i * 82 - 25" width="152" height="50" rx="9" />
          <circle class="vnode-dot" cx="34" :cy="56 + i * 82 - 8" r="3.4" :fill="v.hue" />
          <text class="vnode-name" x="46" :y="56 + i * 82 - 4">{{ v.name }}</text>
          <text class="vnode-model num" x="34" :y="56 + i * 82 + 14">{{ v.models[0] }}</text>
        </g>

        <!-- 网关枢纽：脉冲涟漪 + 主节点 -->
        <circle class="ripple" cx="400" cy="170" r="44" />
        <circle class="ripple r2" cx="400" cy="170" r="44" />
        <g class="hub">
          <rect x="330" y="138" width="140" height="64" rx="12" />
          <text class="hub-name" x="400" y="164">token 中转站</text>
          <text class="hub-sub num" x="400" y="184">OpenAI 兼容 · SSE</text>
        </g>

        <!-- 企业节点（客户）与子账号：一层签发、多层使用 -->
        <g v-for="(o, i) in ORGS" :key="'on' + i" class="onode">
          <rect x="546" :y="o.y - 17" width="92" height="34" rx="8" />
          <text class="onode-name" x="592" :y="o.y + 4">{{ o.name }}</text>
        </g>
        <g v-for="(o, i) in ORGS" :key="'sg' + i" class="snode">
          <g v-for="(dy, j) in SUB_OFFSETS" :key="j">
            <rect x="672" :y="o.y + dy - 12" width="68" height="24" rx="6" />
            <text x="706" :y="o.y + dy + 3.5">子账号</text>
          </g>
        </g>
        <!-- 省略号：接入企业不止图中三家 -->
        <g class="more">
          <circle cx="592" cy="313" r="1.3" />
          <circle cx="592" cy="321" r="1.3" />
          <circle cx="592" cy="329" r="1.3" />
        </g>
      </svg>
    </div>

    <!-- 实时请求流 -->
    <div class="mod m3 ticker">
      <div class="ticker-head">
        <span class="ticker-title">请求流</span>
        <span class="ticker-col">tokens</span>
        <span class="ticker-col">延迟</span>
        <span class="ticker-col">消耗</span>
        <span class="ticker-col">状态</span>
      </div>
      <transition-group name="tick" tag="ul" class="ticker-list">
        <li v-for="r in rows" :key="r.id" class="ticker-row">
          <span class="num t-time">{{ r.time }}</span>
          <span class="t-model"><i class="t-dot" :style="{ background: r.hue }"></i>{{ r.model }}</span>
          <span class="num t-tok">{{ r.tokens.toLocaleString('en-US') }}</span>
          <span class="num t-lat">{{ r.latency }} ms</span>
          <span class="num t-cost">{{ r.cost.toLocaleString('en-US') }}</span>
          <span class="t-ok" :class="{ retried: r.retried }">{{ r.retried ? '429→换渠道 ✓' : '✓' }}</span>
        </li>
      </transition-group>
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
.m3 { animation-delay: 0.27s; }
@keyframes mod-rise {
  from { opacity: 0; transform: translateY(12px); }
  to { opacity: 1; transform: none; }
}

/* ---------- 头部 ---------- */
.board-head { padding: 30px 38px 4px; }
.head-left { display: flex; align-items: center; gap: 12px; }
.live {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 10px; font-weight: 700; letter-spacing: 0.14em;
  color: var(--bd-green-ink);
  background: var(--tg-green-wash);
  border: 1px solid var(--tg-green-wash-strong);
  border-radius: 5px; padding: 2px 7px;
}
.live-dot { width: 5px; height: 5px; border-radius: 50%; background: var(--tg-green); animation: blink 1.6s ease-in-out infinite; }
@keyframes blink { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }
.board-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--bd-ink); letter-spacing: 0.01em; }
.head-right { position: absolute; top: 32px; right: 38px; }
.clock { font-size: 12px; color: var(--bd-sub); }

/* 产品介绍（左侧「看板 + 介绍」的介绍部分） */
.board-sub {
  margin: 12px 0 0; max-width: 560px;
  font-size: 13px; line-height: 1.8; color: var(--bd-sub);
}

.relay { display: flex; align-items: baseline; gap: 9px; margin: 16px 0 0; }
.relay-num {
  font-size: 30px; font-weight: 600; color: var(--bd-ink);
}
.relay-unit { font-size: 11.5px; color: var(--bd-muted); }
.relay-rate { font-size: 12px; color: var(--bd-green-ink); }

/* 关键指标条：企业规模 + 稳定性（与 LIVE 徽标同族的浅绿小标签） */
.stats { display: flex; gap: 8px; margin-top: 14px; }
.stat {
  display: inline-flex; align-items: baseline; gap: 3px;
  font-size: 11px; color: var(--bd-sub);
  background: color-mix(in srgb, var(--tg-green-wash) 55%, transparent);
  border: 1px solid var(--tg-green-wash-strong);
  border-radius: 5px; padding: 3px 9px;
}
.stat b { font-weight: 600; color: var(--bd-green-ink); font-size: 12px; }

/* ---------- 调度总览 SVG（吃满剩余高度，居中） ---------- */
.flow-wrap {
  flex: 1 1 auto; min-height: 0;
  display: flex; align-items: center; justify-content: center;
  padding: 10px 38px 22px;
}
.flow { width: 100%; height: 100%; max-width: 820px; display: block; }

.edge {
  fill: none; stroke-width: 1.2; opacity: 0.55;
  stroke-dasharray: 5 9;
  animation: dashflow 1.05s linear infinite;
}
.edge-out { stroke: var(--tg-green); }
@keyframes dashflow { to { stroke-dashoffset: -14; } }

.edge-label { font-size: 10px; opacity: 0.95; }

.pkt { opacity: 0.9; }

.vnode-box {
  fill: var(--tg-paper);
  stroke: color-mix(in srgb, var(--hue) 45%, transparent);
  stroke-width: 1;
}
.vnode-dot { opacity: 0.95; }
.vnode-name { font-size: 13px; font-weight: 600; fill: var(--bd-ink); }
.vnode-model { font-size: 10.5px; fill: var(--bd-sub); }

.ripple {
  fill: none; stroke: rgba(18, 164, 98, 0.35); stroke-width: 1;
  animation: ripple 2.6s ease-out infinite;
}
.ripple.r2 { animation-delay: 1.3s; }
@keyframes ripple {
  0% { r: 40; opacity: 0.5; }
  100% { r: 74; opacity: 0; }
}
/* 网关枢纽 = 右侧登录按钮同款祖母绿实体（左右呼应的锚点） */
.hub rect {
  fill: var(--tg-green);
  filter: drop-shadow(0 4px 12px rgba(18, 164, 98, 0.28));
}
.hub-name { font-size: 14.5px; font-weight: 700; fill: var(--tg-btn-ink); text-anchor: middle; }
.hub-sub { font-size: 10px; fill: rgba(15, 43, 32, 0.78); text-anchor: middle; }

/* 企业节点：签发给客户企业（绿系浅底，层级介于枢纽与子账号之间） */
.onode rect { fill: var(--tg-green-wash); stroke: var(--tg-green-wash-strong); stroke-width: 1; }
.onode-name { font-size: 12px; font-weight: 600; fill: var(--tg-green-ink); text-anchor: middle; }
/* 子账号：最末层级，弱化成浅色小票，只承载「多个」的阵列感 */
.snode rect { fill: color-mix(in srgb, var(--tg-green-wash) 60%, transparent); }
.snode text { font-size: 9.5px; fill: var(--bd-sub); text-anchor: middle; }
.edge-sub { stroke: var(--tg-green); opacity: 0.55; stroke-dasharray: 3 4; }
.more circle { fill: var(--bd-muted); }

/* ---------- 请求流 ticker（整宽账本式页脚，无内嵌框） ---------- */
.ticker {
  /* 表头与数据行共用同一列模板，保证列标签与数字逐列对齐 */
  --tk-cols: 56px 1fr 56px 58px 62px 86px;
  border-top: 1px solid var(--bd-line);
  overflow: hidden;
}
.ticker-head {
  display: grid;
  grid-template-columns: var(--tk-cols);
  align-items: center; gap: 4px;
  padding: 9px 38px 7px;
  border-bottom: 1px solid var(--tg-line);
}
.ticker-title { grid-column: span 2; font-size: 11px; font-weight: 600; letter-spacing: 0.08em; color: var(--bd-sub); }
.ticker-col { font-size: 10px; color: var(--bd-muted); text-align: right; }

.ticker-list { list-style: none; margin: 0; padding: 0 0 6px; position: relative; }
.ticker-row {
  display: grid;
  grid-template-columns: var(--tk-cols);
  align-items: center; gap: 4px;
  padding: 5.5px 38px;
  font-size: 11px;
}
.ticker-row + .ticker-row { border-top: 1px solid var(--tg-line); }
.t-time { color: var(--bd-muted); }
.t-model {
  display: flex; align-items: center; gap: 6px;
  color: var(--bd-ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.t-dot { width: 6px; height: 6px; border-radius: 50%; flex: none; }
.t-tok { color: var(--bd-sub); text-align: right; }
.t-lat { color: var(--bd-muted); text-align: right; }
.t-cost { color: var(--bd-green-ink); text-align: right; }
.t-ok { color: var(--tg-green-ink); font-size: 10.5px; text-align: right; white-space: nowrap; }
.t-ok.retried { color: var(--tg-amber); }

.tick-enter-active { transition: all 0.45s cubic-bezier(0.2, 0.8, 0.3, 1); }
.tick-enter-from { opacity: 0; transform: translateY(-8px); }
.tick-leave-active { display: none; }
.tick-move { transition: transform 0.45s cubic-bezier(0.2, 0.8, 0.3, 1); }

/* ---------- 降级 ---------- */
@media (prefers-reduced-motion: reduce) {
  .mod, .tick-enter-active, .tick-move { animation: none; transition: none; }
  .edge, .pkt, .ripple, .live-dot { animation: none; }
  .pkt { display: none; }
}
</style>
