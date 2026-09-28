<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { apiBillingOverview } from '../../api/platform'
import { fmtQuota } from '../../utils/format'

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

function fmtSigned(v: number): string {
  return v > 0 ? `+${fmtQuota(v)}` : fmtQuota(v)
}
// 期初缺失（首个快照月之前）显示 —
function fmtOpt(v: number | null | undefined): string {
  return v == null ? '—' : fmtQuota(v)
}
</script>

<template>
  <el-card shadow="never" v-loading="loading">
    <template #header>
      <div class="card-header">
        <span>账单查询（月度总览）</span>
        <div>
          <el-select v-model="month" style="width: 130px; margin-right: 8px" @change="load">
            <el-option v-for="m in monthOptions" :key="m" :label="m" :value="m" />
          </el-select>
        </div>
      </div>
    </template>

    <el-alert v-if="ov" type="info" :closable="false" style="margin-bottom: 14px"
      :title="`账期 ${ov.month}（时区 ${ov.timezone}）· 全部客户 ${rows.length} 家。勾稽口径：期末已用 − 期初已用 == 期内消耗；月边界按账期时区。点击客户名进入其三段式对账单。`" />

    <div v-if="ov" class="totals">
      <div class="total-item">
        <div class="total-label">合计消耗（token）</div>
        <div class="total-value num">{{ fmtQuota(ov.total_consumption) }}</div>
      </div>
      <div class="total-item">
        <div class="total-label">合计请求数</div>
        <div class="total-value num">{{ ov.total_requests }}</div>
      </div>
      <div class="total-item">
        <div class="total-label">期内授权</div>
        <div class="total-value num green">{{ fmtSigned(ov.total_granted) }}</div>
      </div>
      <div class="total-item">
        <div class="total-label">期内冲减</div>
        <div class="total-value num red">{{ fmtSigned(ov.total_revoked) }}</div>
      </div>
      <div class="total-item">
        <div class="total-label">不计量笔数</div>
        <div class="total-value num">{{ ov.total_no_usage }}</div>
      </div>
    </div>

    <el-table :data="rows" empty-text="该账期还没有任何客户。">
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
      <el-table-column label="期初已用" min-width="110">
        <template #default="{ row }">
          <span class="num">{{ fmtOpt(row.opening_used) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="期内授权" min-width="110">
        <template #default="{ row }">
          <span class="num green">{{ fmtSigned(row.total_granted) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="期内冲减" min-width="110">
        <template #default="{ row }">
          <span class="num red">{{ fmtSigned(row.total_revoked) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="消耗（token）" min-width="130" sortable :sort-by="'consumption'">
        <template #default="{ row }">
          <span class="num">{{ fmtQuota(row.consumption) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="期末已用" min-width="110">
        <template #default="{ row }">
          <span class="num">{{ fmtQuota(row.closing_used) }}</span>
          <span v-if="row.closing_is_live" class="dim">（实时）</span>
        </template>
      </el-table-column>
      <el-table-column label="勾稽" width="70" align="center">
        <template #default="{ row }">
          <span v-if="row.chain_ok == null" class="dim">—</span>
          <span v-else-if="row.chain_ok" class="green">✓</span>
          <span v-else class="red">✗</span>
        </template>
      </el-table-column>
      <el-table-column prop="requests" label="请求数" width="90" align="center" />
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
