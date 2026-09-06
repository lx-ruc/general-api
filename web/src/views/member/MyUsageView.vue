<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { apiMyUsage, apiMyStats } from '../../api/member'
import StatCard from '../../components/StatCard.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtPoints, fmtTime, pointsToYuan } from '../../utils/format'

const stats = ref<any>(null)
const list = ref<any[]>([])
const total = ref(0)
const filters = reactive({ page: 1, page_size: 20, model: '' })

async function load() {
  const resp = await apiMyUsage(filters)
  list.value = resp.list
  total.value = resp.total
}
onMounted(async () => {
  stats.value = await apiMyStats()
  load()
})

const chartOption = (d: any) => {
  if (!d) return {}
  return {
    tooltip: { trigger: 'axis' },
    grid: { left: 50, right: 20, top: 30, bottom: 30 },
    xAxis: { type: 'category', data: d.series.map((p: any) => p.date.slice(5)) },
    yAxis: { type: 'value', name: '请求数' },
    series: [{ name: '请求数', type: 'line', smooth: true, areaStyle: {}, data: d.series.map((p: any) => p.requests) }],
  }
}
</script>

<template>
  <div v-if="stats">
    <el-row :gutter="16">
      <el-col :span="8"><StatCard title="今日请求" :value="fmtNum(stats.today.requests)" icon="TrendCharts"
        :sub="`累计 ${fmtNum(stats.total.requests)}`" /></el-col>
      <el-col :span="8"><StatCard title="今日 tokens" :value="fmtNum(stats.today.tokens)" icon="Coin"
        :sub="`累计 ${fmtNum(stats.total.tokens)}`" /></el-col>
      <el-col :span="8"><StatCard title="今日成本" :value="fmtPoints(stats.today.cost) + ' 点'" icon="Money"
        :sub="`¥${pointsToYuan(stats.today.cost)}`" /></el-col>
    </el-row>

    <el-card shadow="never" style="margin: 16px 0">
      <template #header>近 7 日请求趋势</template>
      <LineChart :option="chartOption(stats)" />
    </el-card>

    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>我的调用记录</span>
          <el-input v-model="filters.model" placeholder="按模型过滤" clearable style="width: 180px"
            @keyup.enter="filters.page = 1; load()" />
        </div>
      </template>
      <el-table :data="list" size="small">
        <el-table-column prop="id" label="#" width="70" />
        <el-table-column prop="model_name" label="模型" width="140" />
        <el-table-column label="流式" width="60">
          <template #default="{ row }">{{ row.is_stream ? '是' : '否' }}</template>
        </el-table-column>
        <el-table-column label="tokens(入/出)" width="120">
          <template #default="{ row }">{{ row.prompt_tokens }} / {{ row.completion_tokens }}</template>
        </el-table-column>
        <el-table-column label="成本" width="110">
          <template #default="{ row }">
            <span v-if="row.no_usage" style="color: #e6a23c">未计量</span>
            <template v-else>{{ fmtPoints(row.cost) }} 点</template>
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
.card-header { display: flex; justify-content: space-between; align-items: center; }
</style>
