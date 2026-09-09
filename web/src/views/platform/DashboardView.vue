<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { apiStatsOverview } from '../../api/platform'
import StatRow from '../../components/StatRow.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtQuota, fmtTokenCompact, pointsToYuan } from '../../utils/format'
import { trendOptions, barOption } from '../../utils/chart'

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
      { label: '今日成本', value: fmtQuota(data.today.cost), tone: 'green', sub: `¥${pointsToYuan(data.today.cost)} · 累计 ¥${pointsToYuan(data.total.cost)}` },
      { label: '今日失败', value: fmtNum(data.today.errors), tone: data.today.errors > 0 ? 'danger' : 'default', sub: `累计 ${fmtNum(data.total.errors)}` },
    ]" />

    <StatRow v-if="data.total.vendor_cost > 0 || data.today.vendor_cost > 0" :items="[
      { label: '今日营收', value: fmtQuota(data.today.cost), tone: 'green', sub: `累计 ¥${pointsToYuan(data.total.cost)}` },
      { label: '今日厂商成本', value: fmtQuota(data.today.vendor_cost), sub: `累计 ¥${pointsToYuan(data.total.vendor_cost)}` },
      { label: '今日毛利', value: fmtQuota(data.today.cost - data.today.vendor_cost), tone: 'green',
        sub: `累计毛利 ¥${pointsToYuan(data.total.cost - data.total.vendor_cost)}` },
      { label: '累计毛利率', value: (data.total.cost > 0 ? ((data.total.cost - data.total.vendor_cost) / data.total.cost * 100).toFixed(1) : '0') + '%', tone: 'green' },
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
          <template #header>近 7 日成本<span class="unit">（token）</span></template>
          <LineChart :option="trendOptions(data.series).costOption" />
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>客户消耗 Top</template>
          <LineChart v-if="data.by_org.length"
            :option="barOption(data.by_org.map((o: any) => o.name), data.by_org.map((o: any) => o.cost))"
            height="180px" />
          <div class="chart-gap"></div>
          <el-table :data="data.by_org" size="small">
            <el-table-column prop="name" label="客户" />
            <el-table-column prop="requests" label="请求数" width="90" align="right" />
            <el-table-column label="营收" width="170" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
                <span class="dim"> · ¥{{ pointsToYuan(row.cost) }}</span>
              </template>
            </el-table-column>
            <el-table-column label="毛利" width="140" align="right">
              <template #default="{ row }">
                <span class="num green">¥{{ pointsToYuan(row.profit) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>模型消耗 Top</template>
          <LineChart v-if="data.by_model.length"
            :option="barOption(data.by_model.map((m: any) => m.name || '未路由'), data.by_model.map((m: any) => m.cost))"
            height="180px" />
          <div class="chart-gap"></div>
          <el-table :data="data.by_model" size="small">
            <el-table-column prop="name" label="模型">
              <template #default="{ row }">
                <span :class="{ dim: !row.name }">{{ row.name || '（未路由）' }}</span>
              </template>
            </el-table-column>
            <el-table-column prop="requests" label="请求数" width="90" align="right" />
            <el-table-column label="成本" width="170" align="right">
              <template #default="{ row }">
                <div class="cost-lines">
                  <span><span class="num green">{{ fmtTokenCompact(row.cost) }}</span> <span class="dim">token</span></span>
                  <span class="dim">¥{{ pointsToYuan(row.cost) }}</span>
                </div>
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
.chart-gap { height: 12px; }
.unit { font-size: 12px; color: var(--tg-muted); font-weight: 400; margin-left: 4px; }
.green { color: var(--tg-green-ink); }
.dim { color: var(--tg-muted); font-size: 12px; }
/* Top 表成本列：token 一行、金额一行，避免横向折行 */
.cost-lines { display: inline-flex; flex-direction: column; align-items: flex-end; line-height: 1.6; }
</style>
