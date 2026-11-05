<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiListModelRequests, apiHandleModelRequest, apiListOrgs, apiGetOrgModels, apiSetOrgModels,
  type ModelRequestRow,
} from '../../api/platform'
import { fmtTime } from '../../utils/format'

// 厂商开通：① 审批客户提交的开通申请（批准=写入客户厂商白名单，组内全部模型可用、
// 新增模型自动继承）；② 手动直接调整某客户的开通范围（回收级联移除子账号授权）。

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
    `确认为「${row.org_name}」开通厂商：${row.model_names}？批准后组内全部模型立即可授权（新增模型自动继承）。`,
    '批准开通', { type: 'warning' },
  )
  const r = await apiHandleModelRequest(row.id, 'approve')
  ElMessage.success(r.message || '已批准并开通')
  window.dispatchEvent(new Event('model-requests-changed'))
  load()
}

async function reject(row: ModelRequestRow) {
  const { value } = await ElMessageBox.prompt('驳回原因（将作为平台回复展示给客户）', '驳回申请', {
    inputPlaceholder: '如：该厂商暂不对外开放',
    inputValidator: (v: string) => (v && v.trim() ? true : '请填写驳回原因'),
  })
  await apiHandleModelRequest(row.id, 'reject', value.trim())
  ElMessage.success('已驳回')
  window.dispatchEvent(new Event('model-requests-changed'))
  load()
}

// ---- 手动开通管理（厂商勾选 + 模型例外） ----
const orgs = ref<any[]>([])
const orgId = ref<number | null>(null)
// granted 双段：厂商 id 列表 + 模型级例外名列表
const grantedVendors = ref<number[]>([])
const grantedModels = ref<string[]>([])
const grantedVendorNames = ref<string[]>([]) // 已开通但候选里没出现的厂商（如全部模型停用）
const available = ref<{ name: string; display_name: string; vendor: string; vendor_id: number }[]>([])
const grantSaving = ref(false)

// 候选厂商 = 可用模型的厂商去重 ∪ 已开通厂商
interface VendorOpt { id: number; name: string; modelCount: number }
const vendorOptions = computed<VendorOpt[]>(() => {
  const map = new Map<number, VendorOpt>()
  for (const m of available.value) {
    if (!m.vendor_id) continue
    const cur = map.get(m.vendor_id)
    if (cur) cur.modelCount++
    else map.set(m.vendor_id, { id: m.vendor_id, name: m.vendor || `厂商#${m.vendor_id}`, modelCount: 1 })
  }
  // 已开通但无在列模型的厂商也保留（可勾选/取消）
  availableVendorNames.value.forEach((name, i) => {
    const id = grantedVendors.value[i]
    if (!map.has(id)) map.set(id, { id, name, modelCount: 0 })
  })
  return [...map.values()].sort((a, b) => a.name.localeCompare(b.name))
})
const availableVendorNames = ref<string[]>([])

function toggleVendor(id: number, checked: boolean) {
  grantedVendors.value = checked ? [...grantedVendors.value, id] : grantedVendors.value.filter((v) => v !== id)
}
function toggleModel(name: string, checked: boolean) {
  grantedModels.value = checked ? [...grantedModels.value, name] : grantedModels.value.filter((n) => n !== name)
}
// 组内厂商已勾选时模型例外勾选不产生额外效果（展示为已覆盖）
function coveredByVendor(m: { vendor_id: number }): boolean {
  return grantedVendors.value.includes(m.vendor_id)
}

async function loadOrgModels() {
  if (!orgId.value) return
  const r = await apiGetOrgModels(orgId.value)
  const g = r.granted as any
  grantedVendors.value = (g?.vendors || []).map((v: any) => v.id)
  grantedVendorNames.value = (g?.vendors || []).map((v: any) => v.name)
  availableVendorNames.value = grantedVendorNames.value
  grantedModels.value = g?.models || []
  available.value = r.available
}

async function onOrgChange() {
  grantedVendors.value = []
  grantedModels.value = []
  available.value = []
  loadOrgModels()
}

async function saveGrant() {
  if (!orgId.value) return
  const beforeV = new Set(grantedVendors.value)
  const beforeM = new Set(grantedModels.value)
  const r = await apiGetOrgModels(orgId.value)
  const g = r.granted as any
  const removedV: string[] = []
  ;(g?.vendors || []).forEach((v: any) => { if (!beforeV.has(v.id)) removedV.push(v.name) })
  const removedM = (g?.models || []).filter((n: string) => !beforeM.has(n))
  if (removedV.length > 0 || removedM.length > 0) {
    const parts: string[] = []
    if (removedV.length) parts.push(`厂商 ${removedV.join('、')}`)
    if (removedM.length) parts.push(`模型 ${removedM.join('、')}`)
    await ElMessageBox.confirm(
      `本次保存将回收${parts.join('与')}。该客户全部子账号的相应授权会同步移除，相关调用立即被拒绝，确认继续？`,
      '回收确认', { type: 'warning', confirmButtonText: '确认回收' },
    )
  }
  grantSaving.value = true
  try {
    const resp = await apiSetOrgModels(orgId.value, {
      vendors: grantedVendors.value, models: grantedModels.value,
    } as any)
    ElMessage.success((resp as any).message || '已保存')
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
        <el-table-column label="申请厂商" min-width="200">
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
      <template #header>客户开通管理<span class="unit">（不经申请直接开通/回收；勾厂商=组内全部模型可用，新模型自动继承）</span></template>
      <el-form inline>
        <el-form-item label="客户">
          <el-select v-model="orgId" filterable placeholder="选择客户" style="width: 220px" @change="onOrgChange">
            <el-option v-for="o in orgs" :key="o.id" :label="o.name" :value="o.id" />
          </el-select>
        </el-form-item>
      </el-form>
      <template v-if="orgId">
        <div class="section-title">厂商（推荐按厂商开通）</div>
        <el-table :data="vendorOptions" size="small" empty-text="暂无厂商（先在「厂商管理」接入模型）">
          <el-table-column width="50" align="center">
            <template #default="{ row }">
              <el-checkbox :model-value="grantedVendors.includes(row.id)"
                @change="(v: any) => toggleVendor(row.id, !!v)" />
            </template>
          </el-table-column>
          <el-table-column label="厂商" min-width="140">
            <template #default="{ row }"><b>{{ row.name }}</b></template>
          </el-table-column>
          <el-table-column label="组内模型数" width="100" align="center">
            <template #default="{ row }">{{ row.modelCount }}</template>
          </el-table-column>
        </el-table>

        <div class="section-title">模型级例外（个别模型单独开通；所属厂商已勾选时无需再勾）</div>
        <el-table :data="available" size="small" empty-text="平台暂无已上线的模型">
          <el-table-column width="50" align="center">
            <template #default="{ row }">
              <el-checkbox :model-value="coveredByVendor(row) || grantedModels.includes(row.name)"
                :disabled="coveredByVendor(row)"
                @change="(v: any) => toggleModel(row.name, !!v)" />
            </template>
          </el-table-column>
          <el-table-column label="模型名" min-width="160">
            <template #default="{ row }"><code>{{ row.name }}</code></template>
          </el-table-column>
          <el-table-column prop="display_name" label="说明" min-width="140" />
          <el-table-column prop="vendor" label="厂商" width="120" />
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
.section-title { font-size: 13px; font-weight: 600; margin: 12px 0 6px; }
.dim { color: var(--tg-muted); font-size: 12px; }
</style>
