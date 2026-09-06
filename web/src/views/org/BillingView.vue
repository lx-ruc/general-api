<script setup lang="ts">
import { onMounted, ref } from 'vue'
import dayjs from 'dayjs'
import { apiOrgBilling } from '../../api/org'
import StatRow from '../../components/StatRow.vue'
import { fmtNum, fmtQuota, pointsToYuan } from '../../utils/format'

const month = ref(dayjs().format('YYYY-MM'))
const data = ref<any>(null)
const loading = ref(false)

const monthOptions = (() => {
  const out: string[] = []
  for (let i = 0; i < 12; i++) out.push(dayjs().subtract(i, 'month').format('YYYY-MM'))
  return out
})()

async function load() {
  loading.value = true
  try {
    data.value = await apiOrgBilling(month.value)
  } finally {
    loading.value = false
  }
}
onMounted(load)

function exportCSV() {
  const d = data.value
  if (!d) return
  const lines = [
    `月份,${d.month}`,
    '汇总,请求数,tokens,消耗（平台营收）',
    `合计,${d.summary.requests},${d.summary.tokens},${d.summary.cost}`,
    '',
    '模型,请求数,tokens,消耗',
    ...d.by_model.map((m: any) => `${m.name},${m.requests},${m.tokens},${m.cost}`),
    '',
    '充值时间,金额（token）,折合金额,状态,凭证说明',
    ...d.recharges.map((r: any) =>
      `${dayjs.unix(r.created_at).format('YYYY-MM-DD HH:mm')},${r.amount},${pointsToYuan(r.amount)},${r.status},${(r.voucher || '').replace(/[,\n]/g, ' ')}`),
  ]
  const blob = new Blob(['﻿' + lines.join('\n')], { type: 'text/csv;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `对账单_${d.month}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
}
</script>

<template>
  <div v-loading="loading" class="wrap">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>月度对账单</span>
          <div>
            <el-select v-model="month" style="width: 130px; margin-right: 8px" @change="load">
              <el-option v-for="m in monthOptions" :key="m" :label="m" :value="m" />
            </el-select>
            <el-button @click="exportCSV">导出 CSV</el-button>
          </div>
        </div>
      </template>
      <StatRow v-if="data" :items="[
        { label: '本月请求数', value: fmtNum(data.summary.requests) },
        { label: '本月 tokens', value: fmtNum(data.summary.tokens) },
        { label: '本月消耗', value: fmtQuota(data.summary.cost), tone: 'green', sub: `¥${pointsToYuan(data.summary.cost)}` },
        { label: '本月充值', value: fmtQuota(data.recharged), tone: 'green', sub: `¥${pointsToYuan(data.recharged)}` },
      ]" />
    </el-card>

    <el-row v-if="data" :gutter="16">
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>按模型明细</template>
          <el-table :data="data.by_model" size="small" empty-text="本月无调用">
            <el-table-column prop="name" label="模型" min-width="140" />
            <el-table-column prop="requests" label="请求数" width="80" align="right" />
            <el-table-column label="tokens" width="110" align="right">
              <template #default="{ row }"><span class="num">{{ fmtNum(row.tokens) }}</span></template>
            </el-table-column>
            <el-table-column label="消耗" min-width="150" align="right">
              <template #default="{ row }">
                <span class="num green">{{ fmtQuota(row.cost) }}</span>
                <span class="dim"> · ¥{{ pointsToYuan(row.cost) }}</span>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
      <el-col :xs="24" :md="12">
        <el-card shadow="never">
          <template #header>本月充值记录</template>
          <el-table :data="data.recharges" size="small" empty-text="本月无充值">
            <el-table-column label="时间" width="150">
              <template #default="{ row }">{{ dayjs.unix(row.created_at).format('MM-DD HH:mm') }}</template>
            </el-table-column>
            <el-table-column label="金额" width="150">
              <template #default="{ row }">¥{{ pointsToYuan(row.amount) }}</template>
            </el-table-column>
            <el-table-column label="状态" width="80">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status === 'approved' ? 'success' : row.status === 'pending' ? 'warning' : 'danger'">
                  {{ row.status === 'approved' ? '已到账' : row.status === 'pending' ? '待确认' : '已驳回' }}
                </el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="voucher" label="凭证" min-width="120" show-overflow-tooltip />
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; }
.green { color: var(--tg-green-ink); }
.dim { color: var(--tg-muted); font-size: 12px; }
</style>
