<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiMyRequests, apiCreateRequest, apiMyModels } from '../../api/member'
import { fmtPoints, fmtTime, pointsToYuan } from '../../utils/format'

const me = ref<any>(null)
const list = ref<any[]>([])
const form = reactive({ amount: 1000000, reason: '' })
const submitting = ref(false)

async function load() {
  me.value = await apiMyModels()
  list.value = await apiMyRequests()
}
onMounted(load)

async function submit() {
  if (!form.amount || form.amount <= 0) {
    ElMessage.warning('请填写申请点数')
    return
  }
  submitting.value = true
  try {
    await apiCreateRequest(form.amount, form.reason)
    ElMessage.success('申请已提交，等待公司管理员审批')
    form.amount = 1000000
    form.reason = ''
    load()
  } finally {
    submitting.value = false
  }
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待审批', approved: '已批准', rejected: '已驳回' }[s] || s)
</script>

<template>
  <div v-if="me">
    <el-card shadow="never" style="margin-bottom: 16px">
      <template #header>我的额度</template>
      <el-descriptions :column="3" border>
        <el-descriptions-item label="额度上限">
          {{ me.quota_limit == null ? '不限额' : `${fmtPoints(me.quota_limit)} 点（¥${pointsToYuan(me.quota_limit)}）` }}
        </el-descriptions-item>
        <el-descriptions-item label="已消耗">{{ fmtPoints(me.quota_used) }} 点（¥{{ pointsToYuan(me.quota_used) }}）</el-descriptions-item>
        <el-descriptions-item label="剩余">
          <span v-if="me.quota_limit == null" style="color: #67c23a">不限</span>
          <span v-else :style="{ color: me.quota_limit - me.quota_used > 0 ? '#67c23a' : '#f56c6c', fontWeight: 600 }">
            {{ fmtPoints(me.quota_limit - me.quota_used) }} 点（¥{{ pointsToYuan(me.quota_limit - me.quota_used) }}）
          </span>
        </el-descriptions-item>
      </el-descriptions>

      <el-divider content-position="left">发起额度申请</el-divider>
      <el-form inline>
        <el-form-item label="申请点数">
          <el-input-number v-model="form.amount" :min="100000" :step="1000000" />
          <span class="tip">= ¥{{ pointsToYuan(form.amount) }}</span>
        </el-form-item>
        <el-form-item label="理由">
          <el-input v-model="form.reason" placeholder="如：XX 项目联调需要" style="width: 260px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="submitting" @click="submit">提交申请</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card shadow="never">
      <template #header>申请记录</template>
      <el-table :data="list">
        <el-table-column prop="id" label="#" width="60" />
        <el-table-column label="申请额度" width="170">
          <template #default="{ row }">{{ fmtPoints(row.amount) }} 点（¥{{ pointsToYuan(row.amount) }}）</template>
        </el-table-column>
        <el-table-column prop="reason" label="理由" min-width="150" show-overflow-tooltip />
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)">{{ statusName(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reply" label="审批回复" min-width="120" show-overflow-tooltip />
        <el-table-column label="申请时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
</style>
