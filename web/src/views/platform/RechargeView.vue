<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiListRecharges, apiHandleRecharge, apiGetBankInfo, apiUpdateBankInfo } from '../../api/platform'
import { fmtTime, fmtQuota, pointsToYuan } from '../../utils/format'

const list = ref<any[]>([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({ page: 1, page_size: 20, status: '' })
const bankInfo = ref('')

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
onMounted(async () => {
  load()
  const r = await apiGetBankInfo()
  bankInfo.value = r.bank_info || ''
})

async function saveBank() {
  await apiUpdateBankInfo(bankInfo.value)
  ElMessage.success('收款信息已保存，客户端发起充值时可见')
}

const replyVisible = ref(false)
const action = ref<'approve' | 'reject'>('approve')
const current = ref<any>(null)
const reply = ref('')
function open(r: any, act: 'approve' | 'reject') {
  current.value = r
  action.value = act
  reply.value = ''
  replyVisible.value = true
}
async function submit() {
  if (!current.value) return
  const r = await apiHandleRecharge(current.value.id, action.value, reply.value)
  ElMessage.success(r.message || '已处理')
  replyVisible.value = false
  load()
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待确认', approved: '已到账', rejected: '已驳回' }[s] || s)
</script>

<template>
  <div class="wrap">
    <el-card shadow="never" class="bank">
      <template #header>
        <div class="card-header">
          <span>收款信息（客户端充值时展示）</span>
          <el-button size="small" type="primary" plain @click="saveBank">保存</el-button>
        </div>
      </template>
      <el-input v-model="bankInfo" type="textarea" :rows="3"
        placeholder="对公转账信息，如：&#10;开户名：XX科技有限公司&#10;开户行：XX银行XX支行&#10;账号：1234 5678 9012&#10;备注请注明客户名称" />
    </el-card>

    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>充值审批</span>
          <el-radio-group v-model="filters.status" @change="filters.page = 1; load()">
            <el-radio-button value="">全部</el-radio-button>
            <el-radio-button value="pending">待确认</el-radio-button>
            <el-radio-button value="approved">已到账</el-radio-button>
            <el-radio-button value="rejected">已驳回</el-radio-button>
          </el-radio-group>
        </div>
      </template>
      <el-table :data="list" v-loading="loading" empty-text="暂无充值申请。客户管理员在「充值」页对公转账后提交。">
        <el-table-column prop="id" label="#" width="60" />
        <el-table-column prop="org_name" label="客户" width="120" />
        <el-table-column label="金额" width="180">
          <template #default="{ row }">{{ fmtQuota(row.amount) }}（¥{{ pointsToYuan(row.amount) }}）</template>
        </el-table-column>
        <el-table-column prop="voucher" label="转账凭证说明" min-width="160" show-overflow-tooltip />
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)">{{ statusName(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reply" label="回复" min-width="110" show-overflow-tooltip />
        <el-table-column label="申请时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="160" fixed="right">
          <template #default="{ row }">
            <template v-if="row.status === 'pending'">
              <el-button size="small" type="primary" @click="open(row, 'approve')">确认到账</el-button>
              <el-button size="small" type="danger" @click="open(row, 'reject')">驳回</el-button>
            </template>
          </template>
        </el-table-column>
      </el-table>
      <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
        :total="total" :page-size="filters.page_size" :current-page="filters.page"
        @current-change="(p: number) => { filters.page = p; load() }" />
    </el-card>

    <el-dialog v-model="replyVisible" :title="action === 'approve' ? '确认充值到账' : '驳回充值申请'" width="440px">
      <p v-if="current">
        {{ current.org_name }} · {{ fmtQuota(current.amount) }}（¥{{ pointsToYuan(current.amount) }}）<br />
        <span class="dim">凭证：{{ current.voucher }}</span>
      </p>
      <p v-if="action === 'approve'" class="dim" style="font-size: 12.5px">
        确认后额度自动增加到该客户，并邮件通知其管理员。
      </p>
      <el-input v-model="reply" type="textarea" :rows="2" placeholder="回复（可选）" style="margin-top: 8px" />
      <template #footer>
        <el-button @click="replyVisible = false">取消</el-button>
        <el-button :type="action === 'approve' ? 'primary' : 'danger'" @click="submit">
          {{ action === 'approve' ? '确认到账' : '确认驳回' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; }
.dim { color: var(--tg-muted); font-size: 12.5px; }
</style>
