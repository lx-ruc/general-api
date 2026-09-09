<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { apiStatsOverview } from '../../api/platform'
import StatRow from '../../components/StatRow.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtTokenCompact } from '../../utils/format'
import { trendOptions, barOption } from '../../utils/chart'

const data = ref<any>(null)

onMounted(async () => {
  data.value = await apiStatsOverview()
})

// 卡片口径切换：今日 / 累计
const mode = ref<'today' | 'total'>('today')
const cards = computed(() => {
  if (!data.value) return []
  const t = mode.value === 'today' ? data.value.today : data.value.total
  return [
    { label: mode.value === 'today' ? '今日请求' : '累计请求', value: fmtNum(t.requests) },
    { label: mode.value === 'today' ? '今日 tokens' : '累计 tokens', value: fmtNum(t.tokens), tone: 'green' },
    { label: '调用成功率', value: succRate(t), tone: succTone(t) },
  ]
})

// 调用成功率：成功请求 / 总请求（无请求时显示 —）
function succRate(t: any): string {
  if (!t.requests) return '—'
  const pct = ((t.requests - t.errors) / t.requests) * 100
  return `${Math.min(100, Math.floor(pct * 10) / 10)}%`
}
function succTone(t: any): 'green' | 'default' | 'danger' {
  if (!t.requests) return 'default'
  const pct = ((t.requests - t.errors) / t.requests) * 100
  return pct >= 99 ? 'green' : pct >= 95 ? 'default' : 'danger'
}
</script>

<template>
  <div v-if="data" class="dash">
    <div class="statbar">
      <span class="statbar-title">用量概览</span>
      <el-radio-group v-model="mode" size="small">
        <el-radio-button value="today">今日</el-radio-button>
        <el-radio-button value="total">累计</el-radio-button>
      </el-radio-group>
    </div>
    <StatRow :items="cards" />

    <el-row :gutter="16" class="charts">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日请求</template>
          <LineChart :option="trendOptions(data.series).reqOption" />
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日 tokens</template>
          <LineChart :option="trendOptions(data.series).tokensOption" />
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>客户用量 Top</template>
          <LineChart v-if="data.by_org.length"
            :option="barOption(data.by_org.map((o: any) => o.name), data.by_org.map((o: any) => o.tokens))"
            height="180px" />
          <div class="chart-gap"></div>
          <el-table :data="data.by_org" size="small">
            <el-table-column prop="name" label="客户" />
            <el-table-column prop="requests" label="请求数" width="90" align="right" />
            <el-table-column label="tokens" width="170" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtTokenCompact(row.tokens) }}</span> <span class="dim">token</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>模型用量 Top</template>
          <LineChart v-if="data.by_model.length"
            :option="barOption(data.by_model.map((m: any) => m.name || '未路由'), data.by_model.map((m: any) => m.tokens))"
            height="180px" />
          <div class="chart-gap"></div>
          <el-table :data="data.by_model" size="small">
            <el-table-column prop="name" label="模型">
              <template #default="{ row }">
                <span :class="{ dim: !row.name }">{{ row.name || '（未路由）' }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="requests" label="请求数" width="90" align="right" />
            <el-table-column label="tokens" width="170" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtTokenCompact(row.tokens) }}</span> <span class="dim">token</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped>
.dash { display: flex; flex-direction: column; gap: 16px; }
.statbar { display: flex; justify-content: space-between; align-items: center; }
.statbar-title { font-size: 13px; font-weight: 600; color: var(--tg-graphite); }
.chart-gap { height: 12px; }
.unit { font-size: 12px; color: var(--tg-muted); font-weight: 400; margin-left: 4px; }
.green { color: var(--tg-green-ink); }
.dim { color: var(--tg-muted); font-size: 12px; }
</style>
