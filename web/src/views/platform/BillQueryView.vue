<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { apiBillingOverview } from '../../api/platform'
import { fmtQuota, fmtYuan } from '../../utils/format'

const router = useRouter()

const month = ref(dayjs().format('YYYY-MM'))
const monthOptions = (() => {
  const out: string[] = []
  for (let i = 0; i < 12; i++) out.push(dayjs().subtract(i, 'month').format('YYYY-MM'))
  return out
})()

const ov = ref<any>(null)
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    ov.value = await apiBillingOverview(month.value)
  } finally {
    loading.value = false
  }
}
onMounted(load)

const rows = computed(() => ov.value?.rows || [])

// 口径都用大白话：
// 授权token = 本月给客户新增的额度（发放 − 回收）
// 消耗token = 本月实际用量
// token余额 = 还剩多少可用
// 账单费用 = 本月消耗折算成钱（元）
function granted(r: any): number {
  return (r.total_granted || 0) + (r.total_revoked || 0)
}
function balance(r: any): number {
  return (r.closing_limit || 0) - (r.closing_used || 0)
}

const totals = computed(() => {
  let g = 0, c = 0, b = 0
  for (const r of rows.value) {
    g += granted(r)
    c += r.consumption || 0
    b += balance(r)
  }
  return { granted: g, consumption: c, balance: b }
})
</script>

<template>
  <el-card shadow="never" v-loading="loading">
    <template #header>
      <div class="card-header">
        <span>账单查询</span>
        <el-select v-model="month" style="width: 130px" @change="load">
          <el-option v-for="m in monthOptions" :key="m" :label="m" :value="m" />
        </el-select>
      </div>
    </template>

    <div v-if="ov" class="totals">
      <div class="total-item">
        <div class="total-label">授权token（本月发放）</div>
        <div class="total-value num green">+{{ fmtQuota(totals.granted) }}</div>
      </div>
      <div class="total-item">
        <div class="total-label">消耗token（本月用量）</div>
        <div class="total-value num">{{ fmtQuota(totals.consumption) }}</div>
      </div>
      <div class="total-item">
        <div class="total-label">token余额（全部客户剩余）</div>
        <div class="total-value num green">{{ fmtQuota(totals.balance) }}</div>
      </div>
      <div class="total-item">
        <div class="total-label">账单费用（{{ month }} 合计）</div>
        <div class="total-value num">{{ fmtYuan(totals.consumption) }}</div>
      </div>
    </div>

    <el-table :data="rows" empty-text="该月还没有任何客户。">
      <el-table-column label="客户" min-width="150" fixed="left">
        <template #default="{ row }">
          <el-link type="primary" @click="router.push(`/platform/orgs/${row.org_id}`)">{{ row.org_name }}</el-link>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="86">
        <template #default="{ row }">
          <el-tag v-if="row.status === 1" type="success" size="small">启用</el-tag>
          <el-tag v-else-if="row.status === 2" type="danger" size="small" effect="dark">欠费</el-tag>
          <el-tag v-else type="danger" size="small">停用</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="授权token" min-width="110">
        <template #default="{ row }">
          <span class="num green">{{ granted(row) > 0 ? `+${fmtQuota(granted(row))}` : fmtQuota(granted(row)) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="消耗token" min-width="110">
        <template #default="{ row }">
          <span class="num">{{ fmtQuota(row.consumption) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="token余额" min-width="110">
        <template #default="{ row }">
          <span class="num" :class="balance(row) > 0 ? 'green' : 'red'">{{ fmtQuota(balance(row)) }}</span>
          <span v-if="row.closing_is_live" class="dim">（当前）</span>
        </template>
      </el-table-column>
      <el-table-column label="账单费用（元）" min-width="120">
        <template #default="{ row }">
          <span class="num">{{ fmtYuan(row.consumption) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="110" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="router.push(`/platform/orgs/${row.org_id}`)">查明细</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.dim { color: var(--tg-muted); font-size: 12px; }
.green { color: var(--tg-green-ink); }
.red { color: #c45656; }
.totals {
  display: flex; gap: 28px; flex-wrap: wrap;
  padding: 14px 18px; margin-bottom: 14px;
  background: var(--tg-surface); border: 1px solid var(--tg-line); border-radius: 8px;
}
.total-item { display: flex; flex-direction: column; gap: 4px; }
.total-label { font-size: 12px; color: var(--tg-muted); }
.total-value { font-size: 18px; font-weight: 600; color: var(--tg-ink); }
</style>
