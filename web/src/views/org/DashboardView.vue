<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { apiOrgStats } from '../../api/org'
import StatCard from '../../components/StatCard.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtPoints, pointsToYuan } from '../../utils/format'

const data = ref<any>(null)
const org = ref<any>(null)

onMounted(async () => {
  const resp = await apiOrgStats()
  data.value = resp.overview
  org.value = resp.org
})

const chartOption = (d: any) => {
  if (!d) return {}
  return {
    tooltip: { trigger: 'axis' },
    grid: { left: 50, right: 20, top: 30, bottom: 30 },
    xAxis: { type: 'category', data: d.series.map((p: any) => p.date.slice(5)) },
    yAxis: [
      { type: 'value', name: '请求数' },
      { type: 'value', name: '成本(点)' },
    ],
    series: [
      { name: '请求数', type: 'line', smooth: true, data: d.series.map((p: any) => p.requests) },
      { name: '成本(点)', type: 'line', smooth: true, yAxisIndex: 1, data: d.series.map((p: any) => p.cost) },
    ],
  }
}
</script>

<template>
  <div v-if="data">
    <el-alert v-if="org" type="info" :closable="false" style="margin-bottom: 16px"
      :title="`公司池余额：${fmtPoints(org.quota_limit - org.quota_used)} 点（¥${pointsToYuan(org.quota_limit - org.quota_used)}）`"
      :description="`上限 ${fmtPoints(org.quota_limit)} 点 · 已消耗 ${fmtPoints(org.quota_used)} 点`" />

    <el-row :gutter="16">
      <el-col :span="6"><StatCard title="今日请求" :value="fmtNum(data.today.requests)" icon="TrendCharts"
        :sub="`累计 ${fmtNum(data.total.requests)}`" /></el-col>
      <el-col :span="6"><StatCard title="今日 tokens" :value="fmtNum(data.today.tokens)" icon="Coin"
        :sub="`累计 ${fmtNum(data.total.tokens)}`" /></el-col>
      <el-col :span="6"><StatCard title="今日成本" :value="fmtPoints(data.today.cost) + ' 点'" icon="Money"
        :sub="`¥${pointsToYuan(data.today.cost)}`" /></el-col>
      <el-col :span="6"><StatCard title="今日失败" :value="fmtNum(data.today.errors)" icon="WarningFilled" /></el-col>
    </el-row>

    <el-card shadow="never" style="margin-top: 16px">
      <template #header>近 7 日趋势</template>
      <LineChart :option="chartOption(data)" />
    </el-card>

    <el-row :gutter="16" style="margin-top: 16px">
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>员工消耗 Top</template>
          <el-table :data="data.by_user" size="small" empty-text="暂无数据">
            <el-table-column prop="name" label="用户名" />
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
