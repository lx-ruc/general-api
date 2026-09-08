<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import { apiOrgStats } from '../../api/org'
import StatRow from '../../components/StatRow.vue'
import LineChart from '../../components/LineChart.vue'
import { fmtNum, fmtQuota, pointsToYuan } from '../../utils/format'
import { trendOptions, barOption } from '../../utils/chart'

const data = ref<any>(null)
const org = ref<any>(null)

onMounted(async () => {
  const resp = await apiOrgStats()
  data.value = resp.overview
  org.value = resp.org
})

const opts = computed(() => (data.value ? trendOptions(data.value.series) : null))

// 接入引导：额度 / 子账号 / 调用 三步
const setupSteps = computed(() => {
  if (!org.value || !data.value) return []
  const hasQuota = org.value.quota_limit > 0
  const hasActive = (data.value.by_user || []).length > 0
  const steps = [
    { n: '1', label: '获得额度', hint: hasQuota ? '已开通' : '对公转账充值或联系平台分配', done: hasQuota, link: '/org/recharges' },
    { n: '2', label: '创建子账号并授权模型', hint: '子账号管理 → 新建子账号 → 模型授权', done: hasActive, link: '/org/members' },
    { n: '3', label: '开始调用', hint: '子账号在「我的密钥」创建 key 后即可调用', done: hasActive && data.value.total.requests > 0, link: '/org/usage' },
  ]
  return steps.some((s) => !s.done) ? steps : []
})
</script>

<template>
  <div v-if="data && org" class="dash">
    <!-- 接入引导：三步全部完成后隐藏 -->
    <el-card v-if="setupSteps.length > 0" shadow="never" class="setup">
      <template #header>接入向导</template>
      <div class="setup-row">
        <div v-for="s in setupSteps" :key="s.label" class="setup-step" :class="{ done: s.done }">
          <span class="setup-check">{{ s.done ? '✓' : s.n }}</span>
          <div>
            <div class="setup-label">{{ s.label }}</div>
            <div class="setup-hint">{{ s.hint }}</div>
          </div>
        </div>
      </div>
    </el-card>

    <el-card shadow="never" class="pool">
      <div class="pool-row">
        <div class="pool-item">
          <div class="pool-label">客户池余额</div>
          <div class="pool-value num green">{{ fmtQuota(org.quota_limit - org.quota_used) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">折合金额</div>
          <div class="pool-value num">¥{{ pointsToYuan(org.quota_limit - org.quota_used) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">额度上限</div>
          <div class="pool-value num">{{ fmtQuota(org.quota_limit) }}</div>
        </div>
        <div class="pool-item">
          <div class="pool-label">已消耗</div>
          <div class="pool-value num">{{ fmtQuota(org.quota_used) }}</div>
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
      { label: '今日成本', value: fmtQuota(data.today.cost), tone: 'green', sub: `¥${pointsToYuan(data.today.cost)}` },
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
          <template #header>近 7 日成本<span class="unit">（token）</span></template>
          <LineChart v-if="opts" :option="opts.costOption" />
        </el-card>
      </el-col>
    </el-row>

    <el-row :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>子账号消耗 Top</template>
          <LineChart v-if="data.by_user.length"
            :option="barOption(data.by_user.map((u: any) => u.name), data.by_user.map((u: any) => u.cost))"
            height="180px" />
          <div class="chart-gap"></div>
          <el-table :data="data.by_user" size="small">
            <el-table-column prop="name" label="用户名" />
            <el-table-column prop="requests" label="请求数" width="90" align="right" />
            <el-table-column label="成本" width="170" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
                <span class="dim"> token · ¥{{ pointsToYuan(row.cost) }}</span>
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
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
                <span class="dim"> token · ¥{{ pointsToYuan(row.cost) }}</span>
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
.setup-row { display: flex; gap: 32px; flex-wrap: wrap; }
.setup-step { display: flex; gap: 10px; align-items: flex-start; }
.setup-check {
  width: 22px; height: 22px; border-radius: 50%; flex: none;
  display: flex; align-items: center; justify-content: center;
  font-size: 12px; margin-top: 1px;
  background: var(--tg-green-wash-strong); color: var(--tg-green-ink); font-weight: 600;
}
.setup-step.done .setup-check { background: var(--tg-green); color: #fff; }
.setup-label { font-size: 13.5px; font-weight: 600; color: var(--tg-ink); }
.setup-hint { font-size: 12px; color: var(--tg-muted); margin-top: 2px; }
.chart-gap { height: 12px; }
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
