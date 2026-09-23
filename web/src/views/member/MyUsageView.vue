<script setup lang="ts">
import { onMounted, reactive, ref, computed } from 'vue'
import dayjs from 'dayjs'
import { apiMyUsage, apiMyStats, apiMyUsageBreakdown } from '../../api/member'
import StatRow from '../../components/StatRow.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtQuota, fmtTime, fmtTokensM } from '../../utils/format'
import { trendOptions } from '../../utils/chart'

interface RangeTotals {
  requests: number
  tokens: number
  cost: number
  errors: number
}
interface AggRow {
  id: number
  name: string
  requests: number
  tokens: number
  cost: number
  errors: number
}
interface Breakdown {
  start: number
  end: number
  today: RangeTotals
  month: RangeTotals
  range: RangeTotals
  by_key: AggRow[]
  by_model: AggRow[]
}

const stats = ref<any>(null) // 概览（近 7 日序列 + 今日/累计成功率口径）
const bd = ref<Breakdown | null>(null) // 多维统计（今日/当月/区间 + 按 key/按模型）
const list = ref<any[]>([])
const total = ref(0)

// ---- 分析区间筛选：预设（默认今日）或自定义日期区间 ----
type Preset = 'today' | 'month' | '7d' | 'custom'
const preset = ref<Preset>('today')
const customRange = ref<[string, string] | null>(null) // YYYY-MM-DD
const keyFilter = ref(0) // 调用记录按 key 过滤（0=全部，只影响明细表）
const filters = reactive({ page: 1, page_size: 20, model: '', start: 0, end: 0, key_id: 0 })

// 预设/自定义 → [start, end) unix 秒；custom 未选日期时返回 null（沿用当前区间）
function currentRange(): { start: number; end: number } | null {
  const now = dayjs()
  switch (preset.value) {
    case 'today':
      return { start: now.startOf('day').unix(), end: now.unix() }
    case 'month':
      return { start: now.startOf('month').unix(), end: now.unix() }
    case '7d':
      return { start: now.subtract(6, 'day').startOf('day').unix(), end: now.unix() }
    default:
      if (!customRange.value) return null
      return {
        start: dayjs(customRange.value[0]).startOf('day').unix(),
        end: dayjs(customRange.value[1]).add(1, 'day').startOf('day').unix(),
      }
  }
}

async function load() {
  const resp = await apiMyUsage(filters)
  list.value = resp.list
  total.value = resp.total
}

// 重拉统计与明细（区间变化时）；key 变化只影响明细，走 applyKeyFilter
async function loadAnalysis() {
  const r = currentRange()
  if (!r) return
  filters.page = 1
  filters.start = r.start
  filters.end = r.end
  bd.value = await apiMyUsageBreakdown({ start: r.start, end: r.end })
  await load()
}

function applyKeyFilter() {
  filters.key_id = Number(keyFilter.value) || 0 // 清空下拉得 ''，归零 = 全部
  filters.page = 1
  load()
}

onMounted(async () => {
  stats.value = await apiMyStats()
  await loadAnalysis()
})

// 调用成功率：成功请求 / 总请求（无请求时显示 —）
function succRate(t: { requests: number; errors: number }): string {
  if (!t.requests) return '—'
  const pct = ((t.requests - t.errors) / t.requests) * 100
  return `${Math.min(100, Math.floor(pct * 10) / 10)}%`
}
function succTone(t: { requests: number; errors: number }): 'green' | 'default' | 'danger' {
  if (!t.requests) return 'default'
  const pct = ((t.requests - t.errors) / t.requests) * 100
  return pct >= 99 ? 'green' : pct >= 95 ? 'default' : 'danger'
}

// 概览卡：今日 / 当月固定口径（不受筛选区间影响）
const cards = computed(() => {
  if (!bd.value) return []
  const d = bd.value
  return [
    { label: '今日 tokens', value: fmtTokensM(d.today.tokens), tone: 'green' as const },
    { label: '今日请求', value: fmtNum(d.today.requests) },
    { label: '当月 tokens', value: fmtTokensM(d.month.tokens), tone: 'green' as const },
    { label: '当月请求', value: fmtNum(d.month.requests) },
    { label: '今日成功率', value: succRate(d.today), tone: succTone(d.today) },
  ]
})

// 区间卡：跟随筛选区间
const rangeCards = computed(() => {
  if (!bd.value) return []
  const r = bd.value.range
  return [
    { label: '区间 tokens', value: fmtTokensM(r.tokens), tone: 'green' as const },
    { label: '区间请求', value: fmtNum(r.requests) },
    { label: '区间扣减额度', value: fmtQuota(r.cost) },
    { label: '区间成功率', value: succRate(r), tone: succTone(r) },
  ]
})

const presetNames: Record<Preset, string> = {
  today: '今日', month: '当月', '7d': '近 7 日', custom: '自定义',
}
</script>

<template>
  <div v-if="stats && bd" class="dash">
    <div class="statbar">
      <span class="statbar-title">用量概览</span>
    </div>
    <StatRow :items="cards" />

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日请求</template>
          <LineChart :option="trendOptions(stats.series).reqOption" />
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日 tokens</template>
          <LineChart :option="trendOptions(stats.series).tokensOption" />
        </el-card>
      </el-col>
    </el-row>

    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>用量分析</span>
          <div class="filters">
            <el-radio-group v-model="preset" size="small" @change="loadAnalysis">
              <el-radio-button v-for="(label, key) in presetNames" :key="key" :value="key">{{ label }}</el-radio-button>
            </el-radio-group>
            <el-date-picker v-model="customRange" type="daterange" size="small" style="width: 240px"
              value-format="YYYY-MM-DD" range-separator="→" start-placeholder="开始" end-placeholder="结束"
              :disabled="preset !== 'custom'" @change="loadAnalysis" />
          </div>
        </div>
      </template>
      <StatRow :items="rangeCards" />
      <el-row :gutter="16">
        <el-col :xs="24" :md="12">
          <h4 class="tbl-title">按密钥</h4>
          <el-table :data="bd.by_key" size="small">
            <el-table-column prop="name" label="密钥" min-width="110" show-overflow-tooltip />
            <el-table-column label="请求" width="70" align="right">
              <template #default="{ row }">{{ fmtNum(row.requests) }}</template>
            </el-table-column>
            <el-table-column label="tokens" width="90" align="right">
              <template #default="{ row }">{{ fmtTokensM(row.tokens) }}</template>
            </el-table-column>
            <el-table-column label="扣减额度" width="100" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="成功率" width="70" align="right">
              <template #default="{ row }">{{ succRate(row) }}</template>
            </el-table-column>
          </el-table>
        </el-col>
        <el-col :xs="24" :md="12">
          <h4 class="tbl-title">按模型</h4>
          <el-table :data="bd.by_model" size="small">
            <el-table-column prop="name" label="模型" min-width="110" show-overflow-tooltip />
            <el-table-column label="请求" width="70" align="right">
              <template #default="{ row }">{{ fmtNum(row.requests) }}</template>
            </el-table-column>
            <el-table-column label="tokens" width="90" align="right">
              <template #default="{ row }">{{ fmtTokensM(row.tokens) }}</template>
            </el-table-column>
            <el-table-column label="扣减额度" width="100" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="成功率" width="70" align="right">
              <template #default="{ row }">{{ succRate(row) }}</template>
            </el-table-column>
          </el-table>
        </el-col>
      </el-row>
    </el-card>

    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>调用记录</span>
          <div class="filters">
            <el-select v-model="keyFilter" size="small" style="width: 150px" placeholder="全部密钥"
              clearable @change="applyKeyFilter">
              <el-option v-for="k in bd.by_key" :key="k.id" :label="k.name" :value="k.id" />
            </el-select>
            <el-input v-model="filters.model" placeholder="按模型过滤" clearable style="width: 180px"
              @keyup.enter="filters.page = 1; load()" />
          </div>
        </div>
      </template>
      <el-table :data="list" size="small">
        <el-table-column prop="id" label="#" width="70" />
        <el-table-column prop="model_name" label="模型" width="140" />
        <el-table-column label="流式" width="60">
          <template #default="{ row }">{{ row.is_stream ? '是' : '否' }}</template>
        </el-table-column>
        <el-table-column label="tokens（入 / 出）" width="130" align="right">
          <template #default="{ row }">
            <span class="num" :title="row.cached_tokens > 0 ? `输入中 ${row.cached_tokens} 为缓存命中（已按命中价计费）` : ''">
              {{ row.prompt_tokens }} / {{ row.completion_tokens }}
            </span>
            <span v-if="row.cached_tokens > 0" class="cache-mark">⚡{{ row.cached_tokens }}</span>
          </template>
        </el-table-column>
        <el-table-column label="扣减额度" width="110" align="right">
          <template #default="{ row }">
            <span v-if="row.no_usage" class="warn">未计量</span>
            <span v-else class="num green">{{ fmtQuota(row.cost) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="80">
          <template #default="{ row }">
            <el-tag size="small" :type="row.status < 400 ? 'success' : 'danger'">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="error" label="错误" min-width="120" show-overflow-tooltip />
        <el-table-column label="时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
      </el-table>
      <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
        :total="total" :page-size="filters.page_size" :current-page="filters.page"
        @current-change="(p: number) => { filters.page = p; load() }" />
    </el-card>
  </div>
</template>

<style scoped>
.dash { display: flex; flex-direction: column; gap: 16px; }
.statbar { display: flex; justify-content: space-between; align-items: center; }
.statbar-title { font-size: 13px; font-weight: 600; color: var(--tg-graphite); }
.card-header { display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 8px; }
.filters { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.tbl-title { margin: 14px 0 8px; font-size: 13px; color: var(--tg-graphite); }
.green { color: var(--tg-green-ink); }
.warn { color: var(--tg-amber); font-size: 12px; }
/* 缓存命中 tokens 角标（输入的一部分，按命中价计费） */
.cache-mark { margin-left: 4px; font-size: 11px; color: var(--tg-amber); }
</style>
