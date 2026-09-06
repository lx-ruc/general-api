<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { apiOrgStats } from '../../api/org'
import StatRow from '../../components/StatRow.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtPoints, pointsToYuan } from '../../utils/format'
import { trendOptions } from '../../utils/chart'

const data = ref<any>(null)
const org = ref<any>(null)

onMounted(async () => {
  const resp = await apiOrgStats()
  data.value = resp.overview
  org.value = resp.org
})

const opts = computed(() => (data.value ? trendOptions(data.value.series) : null))
</script>

<template>
  <div v-if="data && org" class="dash">
    <el-card shadow="never" class="pool">
      <div class="pool-row">
        <div class="pool-item">
          <div class="pool-label">公司池余额</div>
          <div class="pool-value num green">{{ fmtPoints(org.quota_limit - org.quota_used) }} <span class="pool-unit">点</span></div>
        </div>
        <div class="pool-item">
          <div class="pool-label">折合金额</div>
          <div class="pool-value num">¥{{ pointsToYuan(org.quota_limit - org.quota_used) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">额度上限</div>
          <div class="pool-value num">{{ fmtPoints(org.quota_limit) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">已消耗</div>
          <div class="pool-value num">{{ fmtPoints(org.quota_used) }}</div>
        </div>
        <div class="pool-meter-wrap">
          <div class="pool-meter" aria-hidden="true">
            <div class="pool-meter-fill" :style="{
              width: `${Math.max(0.8, Math.min(100, org.quota_limit ? (org.quota_used / org.quota_limit) * 100 : 0))}%`,
            }"></div>
          </div>
          <span class="pool-meter-label num">已用 {{ org.quota_limit ? ((org.quota_used / org.quota_limit) * 100).toFixed(2) : '0' }}%</span>
        </div>
      </div>
    </el-card>

    <StatRow :items="[
      { label: '今日请求', value: fmtNum(data.today.requests), sub: `累计 ${fmtNum(data.total.requests)}` },
      { label: '今日 tokens', value: fmtNum(data.today.tokens), sub: `累计 ${fmtNum(data.total.tokens)}` },
      { label: '今日成本', value: fmtPoints(data.today.cost), unit: '点', tone: 'green', sub: `¥${pointsToYuan(data.today.cost)}` },
      { label: '今日失败', value: fmtNum(data.today.errors), tone: data.today.errors > 0 ? 'danger' : 'default' },
    ]" />

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日请求</template>
          <LineChart v-if="opts" :option="opts.reqOption" />
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>近 7 日成本<span class="unit">（点）</span></template>
          <LineChart v-if="opts" :option="opts.costOption" />
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>员工消耗 Top</template>
          <el-table :data="data.by_user" size="small">
            <el-table-column prop="name" label="用户名" />
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

.pool-row { display: flex; align-items: center; gap: 40px; flex-wrap: wrap; }
.pool-item { min-width: 120px; }
.pool-label { font-size: 12px; color: var(--tg-graphite); margin-bottom: 6px; }
.pool-value { font-size: 21px; font-weight: 600; font-variant-numeric: tabular-nums; }
.pool-value.green { color: var(--tg-green-ink); }
.pool-unit { font-size: 12px; color: var(--tg-muted); font-weight: 400; }

.pool-meter-wrap { flex: 1; min-width: 200px; display: flex; align-items: center; gap: 10px; }
.pool-meter {
  flex: 1; height: 8px;
  background: var(--tg-green-wash); border-radius: 4px; overflow: hidden;
}
.pool-meter-label { font-size: 11.5px; color: var(--tg-muted); white-space: nowrap; }
.pool-meter-fill {
  height: 100%; background: var(--tg-green); border-radius: 4px;
  transition: width 0.3s;
}
</style>
