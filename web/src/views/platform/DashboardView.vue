<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { apiStatsOverview } from '../../api/platform'
import StatRow from '../../components/StatRow.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtPoints, pointsToYuan } from '../../utils/format'
import { trendOptions } from '../../utils/chart'

const data = ref<any>(null)

onMounted(async () => {
  data.value = await apiStatsOverview()
})
</script>

<template>
  <div v-if="data" class="dash">
    <StatRow :items="[
      { label: '今日请求', value: fmtNum(data.today.requests), sub: `累计 ${fmtNum(data.total.requests)}` },
      { label: '今日 tokens', value: fmtNum(data.today.tokens), sub: `累计 ${fmtNum(data.total.tokens)}` },
      { label: '今日成本', value: fmtPoints(data.today.cost), unit: '点', tone: 'green', sub: `¥${pointsToYuan(data.today.cost)} · 累计 ¥${pointsToYuan(data.total.cost)}` },
      { label: '今日失败', value: fmtNum(data.today.errors), tone: data.today.errors > 0 ? 'danger' : 'default', sub: `累计 ${fmtNum(data.total.errors)}` },
    ]" />

    <el-row :gutter="16" class="charts">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日请求</template>
          <LineChart :option="trendOptions(data.series).reqOption" />
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日成本<span class="unit">（点，1 元 = 100 万点）</span></template>
          <LineChart :option="trendOptions(data.series).costOption" />
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>公司消耗 Top</template>
          <el-table :data="data.by_org" size="small">
            <el-table-column prop="name" label="公司" />
            <el-table-column prop="requests" label="请求数" width="90" align="right" />
            <el-table-column label="成本" width="170" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtPoints(row.cost) }}</span>
                <span class="dim"> 点 · ¥{{ pointsToYuan(row.cost) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>模型消耗 Top</template>
          <el-table :data="data.by_model" size="small">
            <el-table-column prop="name" label="模型">
              <template #default="{ row }">
                <span :class="{ dim: !row.name }">{{ row.name || '（未路由）' }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="requests" label="请求数" width="90" align="right" />
            <el-table-column label="成本" width="170" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtPoints(row.cost) }}</span>
                <span class="dim"> 点 · ¥{{ pointsToYuan(row.cost) }}</span>
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
.unit { font-size: 12px; color: var(--tg-muted); font-weight: 400; margin-left: 4px; }
.green { color: var(--tg-green-ink); }
.dim { color: var(--tg-muted); font-size: 12px; }
</style>
