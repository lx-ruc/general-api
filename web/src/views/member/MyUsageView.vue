<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { apiMyUsage, apiMyStats } from '../../api/member'
import StatRow from '../../components/StatRow.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtPoints, fmtTime, pointsToYuan } from '../../utils/format'
import { trendOptions } from '../../utils/chart'

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
</script>

<template>
  <div v-if="stats" class="dash">
    <StatRow :items="[
      { label: '今日请求', value: fmtNum(stats.today.requests), sub: `累计 ${fmtNum(stats.total.requests)}` },
      { label: '今日 tokens', value: fmtNum(stats.today.tokens), sub: `累计 ${fmtNum(stats.total.tokens)}` },
      { label: '今日成本', value: fmtPoints(stats.today.cost), unit: '点', tone: 'green', sub: `¥${pointsToYuan(stats.today.cost)}` },
    ]" />

    <el-card shadow="never">
      <template #header>近 7 日请求</template>
      <LineChart :option="trendOptions(stats.series).reqOption" />
    </el-card>

    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>调用记录</span>
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
        <el-table-column label="tokens（入 / 出）" width="130" align="right">
          <template #default="{ row }"><span class="num">{{ row.prompt_tokens }} / {{ row.completion_tokens }}</span></template>
        </el-table-column>
        <el-table-column label="成本" width="110" align="right">
          <template #default="{ row }">
            <span v-if="row.no_usage" class="warn">未计量</span>
            <span v-else class="num green">{{ fmtPoints(row.cost) }} 点</span>
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
.card-header { display: flex; justify-content: space-between; align-items: center; }
.green { color: var(--tg-green-ink); }
.warn { color: var(--tg-amber); font-size: 12px; }
</style>
