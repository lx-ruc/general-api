<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiOrgRequests, apiHandleRequest, type QuotaRequestRow } from '../../api/org'
import { fmtTime, fmtPoints, pointsToYuan } from '../../utils/format'

const list = ref<QuotaRequestRow[]>([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({ page: 1, page_size: 20, status: '' })

async function load() {
  loading.value = true
  try {
    const resp = await apiOrgRequests(filters)
    list.value = resp.list
    total.value = resp.total
  } finally {
    loading.value = false
  }
}
onMounted(load)

const replyVisible = ref(false)
const action = ref<'approve' | 'reject'>('approve')
const current = ref<QuotaRequestRow | null>(null)
const reply = ref('')

function open(r: QuotaRequestRow, act: 'approve' | 'reject') {
  current.value = r
  action.value = act
  reply.value = act === 'approve' ? '已批准' : ''
  replyVisible.value = true
}

async function submit() {
  if (!current.value) return
  await apiHandleRequest(current.value.id, action.value, reply.value)
  ElMessage.success(action.value === 'approve' ? '已批准并追加额度' : '已驳回')
  replyVisible.value = false
  load()
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待审批', approved: '已批准', rejected: '已驳回' }[s] || s)
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>员工额度申请</span>
        <el-radio-group v-model="filters.status" @change="filters.page = 1; load()">
          <el-radio-button value="">全部</el-radio-button>
          <el-radio-button value="pending">待审批</el-radio-button>
          <el-radio-button value="approved">已批准</el-radio-button>
          <el-radio-button value="rejected">已驳回</el-radio-button>
        </el-radio-group>
      </div>
    </template>

    <el-table :data="list" v-loading="loading" empty-text="暂无申请记录。员工额度不足时会在这里发起申请。">
      <el-table-column prop="id" label="#" width="60" />
      <el-table-column prop="username" label="申请人" width="110" />
      <el-table-column label="申请额度" width="170">
        <template #default="{ row }">{{ fmtPoints(row.amount) }} 点（¥{{ pointsToYuan(row.amount) }}）</template>
      </el-table-column>
      <el-table-column prop="reason" label="理由" min-width="150" show-overflow-tooltip />
      <el-table-column label="状态" width="90">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)">{{ statusName(row.status) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="reply" label="回复" min-width="120" show-overflow-tooltip />
      <el-table-column label="申请时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="160" fixed="right">
        <template #default="{ row }">
          <template v-if="row.status === 'pending'">
            <el-button size="small" type="primary" @click="open(row, 'approve')">批准</el-button>
            <el-button size="small" type="danger" @click="open(row, 'reject')">驳回</el-button>
          </template>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="total" :page-size="filters.page_size" :current-page="filters.page"
      @current-change="(p: number) => { filters.page = p; load() }" />
  </el-card>

  <el-dialog v-model="replyVisible" :title="action === 'approve' ? '批准额度申请' : '驳回申请'" width="420px">
    <p v-if="current">
      申请人：{{ current.username }} · 申请 {{ fmtPoints(current.amount) }} 点（¥{{ pointsToYuan(current.amount) }}）<br />
      <span style="color: #909399">{{ current.reason }}</span>
    </p>
    <el-input v-model="reply" type="textarea" :rows="2" placeholder="审批回复（可选）" style="margin-top: 8px" />
    <template #footer>
      <el-button @click="replyVisible = false">取消</el-button>
      <el-button :type="action === 'approve' ? 'primary' : 'danger'" @click="submit">
        {{ action === 'approve' ? '确认批准' : '确认驳回' }}
      </el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
</style>
