<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiListModelRequests, apiHandleModelRequest, apiListOrgs, apiGetOrgModels, apiSetOrgModels,
  type ModelRequestRow,
} from '../../api/platform'
import { fmtTime } from '../../utils/format'

// 模型开通：两件事——① 审批客户提交的开通申请（批准=写入客户白名单，客户管理员
// 立即可给子账号授权）；② 手动直接调整某客户的开通范围（回收会级联移除该客户
// 全部子账号的相应授权，调用立即被拒）。

const list = ref<ModelRequestRow[]>([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({ page: 1, page_size: 20, status: 'pending' })

async function load() {
  loading.value = true
  try {
    const resp = await apiListModelRequests(filters)
    list.value = resp.list
    total.value = resp.total
  } finally {
    loading.value = false
  }
}

async function approve(row: ModelRequestRow) {
  await ElMessageBox.confirm(
    `确认为「${row.org_name}」开通模型：${row.model_names}？批准后客户管理员即可在子账号管理中授权这些模型。`,
    '批准开通', { type: 'warning' },
  )
  const r = await apiHandleModelRequest(row.id, 'approve')
  ElMessage.success(r.message || '已批准并开通')
  window.dispatchEvent(new Event('model-requests-changed'))
  load()
}

async function reject(row: ModelRequestRow) {
  const { value } = await ElMessageBox.prompt('驳回原因（将作为平台回复展示给客户）', '驳回申请', {
    inputPlaceholder: '如：该模型暂不对外开放',
    inputValidator: (v: string) => (v && v.trim() ? true : '请填写驳回原因'),
  })
  await apiHandleModelRequest(row.id, 'reject', value.trim())
  ElMessage.success('已驳回')
  window.dispatchEvent(new Event('model-requests-changed'))
  load()
}

// ---- 手动开通管理 ----
const orgs = ref<any[]>([])
const orgId = ref<number | null>(null)
const granted = ref<string[]>([])
const available = ref<{ name: string; display_name: string; vendor: string }[]>([])
const grantSaving = ref(false)

const grantedSet = () => new Set(granted.value)

function toggleModel(name: string, checked: boolean) {
  granted.value = checked ? [...granted.value, name] : granted.value.filter((n) => n !== name)
}

async function loadOrgModels() {
  if (!orgId.value) return
  const r = await apiGetOrgModels(orgId.value)
  granted.value = r.granted
  available.value = r.available
}

async function onOrgChange() {
  granted.value = []
  available.value = []
  loadOrgModels()
}

async function saveGrant() {
  if (!orgId.value) return
  const before = grantedSet()
  const r = await apiGetOrgModels(orgId.value)
  const removed = r.granted.filter((n: string) => !before.has(n))
  if (removed.length > 0) {
    await ElMessageBox.confirm(
      `本次保存将回收模型：${removed.join('、')}。该客户全部子账号的相应授权会同步移除，相关调用立即被拒绝，确认继续？`,
      '回收确认', { type: 'warning', confirmButtonText: '确认回收' },
    )
  }
  grantSaving.value = true
  try {
    const resp = await apiSetOrgModels(orgId.value, [...granted.value])
    ElMessage.success(resp.message || '已保存')
    loadOrgModels()
  } finally {
    grantSaving.value = false
  }
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待审批', approved: '已开通', rejected: '已驳回' }[s] || s)

onMounted(async () => {
  load()
  const r = await apiListOrgs({ page: 1, page_size: 200 })
  orgs.value = r.list || r || []
})
</script>

<template>
  <div class="wrap">
    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>开通申请</span>
          <el-select v-model="filters.status" clearable placeholder="全部状态" style="width: 130px"
            @change="filters.page = 1; load()">
            <el-option label="待审批" value="pending" />
            <el-option label="已开通" value="approved" />
            <el-option label="已驳回" value="rejected" />
          </el-select>
        </div>
      </template>
      <el-table :data="list" size="small" v-loading="loading" empty-text="暂无开通申请。客户在「模型申请」页提交后会在这里出现。">
        <el-table-column prop="id" label="#" width="70" />
        <el-table-column prop="org_name" label="客户" min-width="120" show-overflow-tooltip />
        <el-table-column label="申请模型" min-width="200">
          <template #default="{ row }">
            <el-tag v-for="n in row.model_names.split(',')" :key="n" size="small" style="margin-right: 4px">{{ n }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="申请说明" min-width="150" show-overflow-tooltip />
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag size="small" :type="statusType(row.status)">{{ statusName(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reply" label="平台回复" min-width="130" show-overflow-tooltip />
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

    <el-card shadow="never">
      <template #header>客户开通管理<span class="unit">（不经申请直接开通/回收）</span></template>
      <el-form inline>
        <el-form-item label="客户">
          <el-select v-model="orgId" filterable placeholder="选择客户" style="width: 220px" @change="onOrgChange">
            <el-option v-for="o in orgs" :key="o.id" :label="o.name" :value="o.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template v-if="orgId">
        <p class="hint" style="margin: 0 0 8px">
          勾选该客户可用的模型。取消勾选并保存 = 回收：该客户全部子账号的相应授权会同步移除，相关调用立即被拒绝。
        </p>
        <el-table :data="available" size="small" empty-text="平台暂无已上线的模型">
          <el-table-column width="50" align="center">
            <template #default="{ row }">
              <el-checkbox :model-value="granted.includes(row.name)"
                @change="(v: any) => toggleModel(row.name, !!v)" />
            </template>
          </el-table-column>
          <el-table-column label="模型名" min-width="160">
            <template #default="{ row }"><code>{{ row.name }}</code></template>
          </el-table-column>
          <el-table-column prop="display_name" label="说明" min-width="160" />
          <el-table-column prop="vendor" label="厂商" width="110" />
        </el-table>
        <el-button type="primary" style="margin-top: 12px" :loading="grantSaving" @click="saveGrant">保存</el-button>
      </template>
    </el-card>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.card-header { display: flex; justify-content: space-between; align-items: center; }
.unit { font-size: 12px; color: #909399; font-weight: normal; }
.hint { font-size: 12px; color: #909399; }
.dim { color: var(--tg-muted); font-size: 12px; }
</style>
