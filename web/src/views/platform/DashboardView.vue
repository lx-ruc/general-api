<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { apiStatsOverview } from '../../api/platform'
import StatCard from '../../components/StatCard.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtPoints, pointsToYuan } from '../../utils/format'

interface Overview {
  today: { requests: number; tokens: number; cost: number; errors: number }
  total: { requests: number; tokens: number; cost: number; errors: number }
  series: { date: string; requests: number; tokens: number; cost: number }[]
  by_org: { id: number; name: string; requests: number; cost: number }[]
  by_model: { name: string; requests: number; cost: number }[]
}

const data = ref<Overview | null>(null)

onMounted(async () => {
  data.value = await apiStatsOverview()
})

const chartOption = (d: Overview | null) => {
  if (!d) return {}
  return {
    tooltip: { trigger: 'axis' },
    grid: { left: 50, right: 20, top: 30, bottom: 30 },
    xAxis: { type: 'category', data: d.series.map((p) => p.date.slice(5)) },
    yAxis: [
      { type: 'value', name: '请求数' },
      { type: 'value', name: '成本(点)' },
    ],
    series: [
      { name: '请求数', type: 'line', smooth: true, data: d.series.map((p) => p.requests) },
      { name: '成本(点)', type: 'line', smooth: true, yAxisIndex: 1, data: d.series.map((p) => p.cost) },
    ],
  }
}
</script>

<template>
  <div v-if="data">
    <el-row :gutter="16">
      <el-col :span="6"><StatCard title="今日请求" :value="fmtNum(data.today.requests)" icon="TrendCharts"
        :sub="`累计 ${fmtNum(data.total.requests)}`" /></el-col>
      <el-col :span="6"><StatCard title="今日 tokens" :value="fmtNum(data.today.tokens)" icon="Coin"
        :sub="`累计 ${fmtNum(data.total.tokens)}`" /></el-col>
      <el-col :span="6"><StatCard title="今日成本" :value="fmtPoints(data.today.cost) + ' 点'"
        icon="Money" :sub="`¥${pointsToYuan(data.today.cost)} · 累计 ¥${pointsToYuan(data.total.cost)}`" /></el-col>
      <el-col :span="6"><StatCard title="今日失败" :value="fmtNum(data.today.errors)" icon="WarningFilled"
        :sub="`累计 ${fmtNum(data.total.errors)}`" /></el-col>
    </el-row>

    <el-card shadow="never" style="margin-top: 16px">
      <template #header>近 7 日趋势</template>
      <LineChart :option="chartOption(data)" />
    </el-card>

    <el-row :gutter="16" style="margin-top: 16px">
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>公司消耗 Top</template>
          <el-table :data="data.by_org" size="small" empty-text="暂无数据">
            <el-table-column prop="name" label="公司" />
            <el-table-column prop="requests" label="请求数" width="100" />
            <el-table-column label="成本" width="150">
              <template #default="{ row }">{{ fmtPoints(row.cost) }} 点（¥{{ pointsToYuan(row.cost) }}）</template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>模型消耗 Top</template>
          <el-table :data="data.by_model" size="small" empty-text="暂无数据">
            <el-table-column prop="name" label="模型" />
            <el-table-column prop="requests" label="请求数" width="100" />
            <el-table-column label="成本" width="150">
              <template #default="{ row }">{{ fmtPoints(row.cost) }} 点（¥{{ pointsToYuan(row.cost) }}）</template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>
