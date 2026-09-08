<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiOrgRecharges, apiCreateRecharge, apiOrgBankInfo } from '../../api/org'
import { fmtTime, fmtQuota, pointsToYuan } from '../../utils/format'

const bankInfo = ref('')
const list = ref<any[]>([])
const form = reactive({ amount: 10000000, voucher: '' })
const submitting = ref(false)

async function load() {
  const r = await apiOrgBankInfo()
  bankInfo.value = r.bank_info || '平台尚未配置收款信息，请联系系统管理员'
  list.value = await apiOrgRecharges()
}
onMounted(load)

async function submit() {
  if (!form.amount || !form.voucher) {
    ElMessage.warning('请填写充值金额与转账凭证说明')
    return
  }
  submitting.value = true
  try {
    const r = await apiCreateRecharge(form.amount, form.voucher)
    ElMessage.success(r.message || '已提交')
    form.voucher = ''
    load()
  } finally {
    submitting.value = false
  }
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待确认', approved: '已到账', rejected: '已驳回' }[s] || s)
</script>

<template>
  <div class="wrap">
    <el-card shadow="never">
      <template #header>充值（对公转账）</template>
      <el-alert type="info" :closable="false" style="margin-bottom: 16px">
        <template #title>第一步：向平台对公转账</template>
        <pre class="bank-info">{{ bankInfo }}</pre>
      </el-alert>
      <el-divider content-position="left">第二步：提交充值申请（平台确认后额度自动到账并邮件通知）</el-divider>
      <el-form inline>
        <el-form-item label="充值金额（token）">
          <el-input-number v-model="form.amount" :min="1000000" :step="5000000" />
          <span class="tip">= ¥{{ pointsToYuan(form.amount) }}</span>
        </el-form-item>
        <el-form-item label="转账凭证">
          <el-input v-model="form.voucher" placeholder="银行流水号 / 转账时间 / 户名" style="width: 260px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="submitting" @click="submit">提交申请</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card shadow="never">
      <template #header>充值记录</template>
      <el-table :data="list" empty-text="暂无充值记录">
        <el-table-column prop="id" label="#" width="60" />
        <el-table-column label="金额" width="180">
          <template #default="{ row }">{{ fmtQuota(row.amount) }}（¥{{ pointsToYuan(row.amount) }}）</template>
        </el-table-column>
        <el-table-column prop="voucher" label="凭证说明" min-width="160" show-overflow-tooltip />
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)">{{ statusName(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reply" label="平台回复" min-width="120" show-overflow-tooltip />
        <el-table-column label="申请时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
.bank-info { margin: 8px 0 0; font-family: inherit; white-space: pre-wrap; font-size: 13px; line-height: 1.9; }
</style>
