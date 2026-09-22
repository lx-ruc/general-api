<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiGetBankInfo, apiHandleRecharge, apiListRecharges, apiUpdateBankInfo, type RechargeRow,
} from '../../api/platform'
import { fmtQuota, fmtTime } from '../../utils/format'

// 充值审批：客户在对公转账后提交申请，平台在这里核对凭证并确认到账。
// 批准走后端 AddOrgQuotaTx（额度+流水同事务）并自动邮件通知客户管理员；
// 收款信息展示在客户侧「充值」页，空值时客户只能看到兜底文案——必须在这里先配好。

const list = ref<RechargeRow[]>([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({ page: 1, page_size: 20, status: '' })

const bankInfo = ref('')
const bankSaving = ref(false)

async function load() {
  loading.value = true
  try {
    const resp = await apiListRecharges(filters)
    list.value = resp.list
    total.value = resp.total
  } finally {
    loading.value = false
  }
}

async function loadBank() {
  const r = await apiGetBankInfo()
  bankInfo.value = r.bank_info || ''
}

async function saveBank() {
  bankSaving.value = true
  try {
    await apiUpdateBankInfo(bankInfo.value)
    ElMessage.success('收款信息已更新，客户侧充值页即时生效')
  } finally {
    bankSaving.value = false
  }
}

async function approve(row: RechargeRow) {
  await ElMessageBox.confirm(
    `确认「${row.org_name}」的充值 ${fmtQuota(row.amount)} 已到账？批准后额度自动入账并邮件通知客户管理员。`,
    '批准充值', { type: 'warning' },
  )
  const r = await apiHandleRecharge(row.id, 'approve')
  ElMessage.success(r.message || '已批准')
  load()
}

async function reject(row: RechargeRow) {
  const { value } = await ElMessageBox.prompt('驳回原因（将作为平台回复展示给客户）', '驳回充值', {
    inputPlaceholder: '如：未查到该笔转账，请核对凭证',
    inputValidator: (v: string) => (v && v.trim() ? true : '请填写驳回原因'),
  })
  await apiHandleRecharge(row.id, 'reject', value.trim())
  ElMessage.success('已驳回')
  load()
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待确认', approved: '已到账', rejected: '已驳回' }[s] || s)

onMounted(() => {
  load()
  loadBank()
})
</script>

<template>
  <div class="wrap">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>收款信息（展示在客户侧「充值」页）</span>
          <el-button type="primary" size="small" :loading="bankSaving" @click="saveBank">保存</el-button>
        </div>
      </template>
      <el-input v-model="bankInfo" type="textarea" :rows="4"
        placeholder="户名 / 开户行 / 账号 / 备注（客户转账时参照，留空则客户侧显示『平台尚未配置收款信息』）" />
    </el-card>

    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>充值申请</span>
          <el-select v-model="filters.status" clearable placeholder="全部状态" style="width: 130px"
            @change="filters.page = 1; load()">
            <el-option label="待确认" value="pending" />
            <el-option label="已到账" value="approved" />
            <el-option label="已驳回" value="rejected" />
          </el-select>
        </div>
      </template>
      <el-table :data="list" size="small" v-loading="loading" empty-text="暂无充值申请。客户在对公转账后会在这里提交。">
        <el-table-column prop="id" label="#" width="70" />
        <el-table-column prop="org_name" label="客户" min-width="120" show-overflow-tooltip />
        <el-table-column label="金额" width="170" align="right">
          <template #default="{ row }">{{ fmtQuota(row.amount) }}</template>
        </el-table-column>
        <el-table-column prop="voucher" label="转账凭证" min-width="180" show-overflow-tooltip />
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="statusType(row.status)">{{ statusName(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reply" label="平台回复" min-width="140" show-overflow-tooltip />
        <el-table-column label="申请时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="130" fixed="right">
          <template #default="{ row }">
            <template v-if="row.status === 'pending'">
              <el-button size="small" type="primary" @click="approve(row)">批准</el-button>
              <el-button size="small" type="danger" @click="reject(row)">驳回</el-button>
            </template>
            <span v-else class="dim">已处理</span>
          </template>
        </el-table-column>
      </el-table>
      <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
        :total="total" :page-size="filters.page_size" :current-page="filters.page"
        @current-change="(p: number) => { filters.page = p; load() }" />
    </el-card>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; }
.dim { color: var(--tg-muted); font-size: 12px; }
</style>
