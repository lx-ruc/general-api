<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  apiModelRequestCandidates, apiOrgModelRequests, apiCreateModelRequest,
  type ModelRequestCandidate, type OrgModelRequestRow,
} from '../../api/org'
import { fmtTime } from '../../utils/format'

// 模型申请（向平台）：客户管理员为客户申请开通新模型，平台审批通过后
// 才能在「子账号管理 → 模型授权」里给子账号勾选。已开通/审批中的模型不可重复申请。

const candidates = ref<ModelRequestCandidate[]>([])
const records = ref<OrgModelRequestRow[]>([])
const submitting = ref(false)
const form = reactive({ models: [] as string[], reason: '' })

const grantedModels = computed(() => candidates.value.filter((m) => m.granted))
// 可勾选 = 未开通且不在途（审批中）
const selectable = computed(() => candidates.value.filter((m) => !m.granted && !m.pending))

async function load() {
  candidates.value = await apiModelRequestCandidates()
  records.value = await apiOrgModelRequests()
}
onMounted(load)

async function submit() {
  if (form.models.length === 0) {
    ElMessage.warning('请勾选要申请开通的模型')
    return
  }
  submitting.value = true
  try {
    await apiCreateModelRequest([...form.models], form.reason)
    ElMessage.success('申请已提交，平台审批通过后即可在子账号管理中授权这些模型')
    form.models = []
    form.reason = ''
    load()
  } finally {
    submitting.value = false
  }
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待审批', approved: '已开通', rejected: '已驳回' }[s] || s)

// 表格行内勾选（已开通/审批中禁选），手动维护选中数组
function toggleModel(name: string, checked: boolean) {
  form.models = checked
    ? [...form.models, name]
    : form.models.filter((n) => n !== name)
}
</script>

<template>
  <div class="wrap">
    <el-card shadow="never">
      <template #header>已开通模型<span class="unit">（由平台开通，可立即给子账号授权）</span></template>
      <template v-if="grantedModels.length > 0">
        <el-tag v-for="m in grantedModels" :key="m.name" style="margin: 0 8px 8px 0">{{ m.name }}</el-tag>
      </template>
      <p v-else class="hint">还没有开通任何模型。请在下方申请，平台审批通过后即可给子账号授权。</p>
    </el-card>

    <el-card shadow="never">
      <template #header>申请开通</template>
      <p class="hint" style="margin: 0 0 8px">勾选需要开通的模型并提交申请；平台审批通过后会站内通知你。</p>
      <el-table :data="candidates" size="small" empty-text="平台暂无已上线的模型">
        <el-table-column width="50" align="center">
          <template #default="{ row }">
            <el-checkbox :model-value="form.models.includes(row.name)"
              :disabled="row.granted || row.pending"
              @change="(v: any) => toggleModel(row.name, !!v)" />
          </template>
        </el-table-column>
        <el-table-column label="模型名" min-width="160">
          <template #default="{ row }"><code>{{ row.name }}</code></template>
        </el-table-column>
        <el-table-column prop="display_name" label="说明" min-width="160" />
        <el-table-column prop="vendor" label="厂商" width="110" />
        <el-table-column label="状态" width="100" align="center">
          <template #default="{ row }">
            <el-tag v-if="row.granted" size="small" type="success">已开通</el-tag>
            <el-tag v-else-if="row.pending" size="small" type="warning">审批中</el-tag>
            <el-tag v-else size="small" type="info" effect="plain">可申请</el-tag>
          </template>
        </el-table-column>
      </el-table>
      <el-form inline style="margin-top: 12px">
        <el-form-item label="申请说明">
          <el-input v-model="form.reason" placeholder="如：新项目需要接入 glm-5.2" style="width: 320px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="submitting" :disabled="selectable.length === 0" @click="submit">
            提交申请
          </el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card shadow="never">
      <template #header>申请记录</template>
      <el-table :data="records" empty-text="还没有提交过开通申请">
        <el-table-column prop="id" label="#" width="60" />
        <el-table-column label="申请模型" min-width="200">
          <template #default="{ row }">
            <el-tag v-for="n in row.model_names.split(',')" :key="n" size="small" style="margin-right: 4px">{{ n }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="申请说明" min-width="150" show-overflow-tooltip />
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)">{{ statusName(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reply" label="平台回复" min-width="140" show-overflow-tooltip />
        <el-table-column label="申请时间" width="160">
          <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
        </el-table-column>
      </el-table>
    </el-card>
  </div>
</template>

<style scoped>
.wrap { display: flex; flex-direction: column; gap: 16px; }
.unit { font-size: 12px; color: #909399; font-weight: normal; }
.hint { font-size: 12px; color: #909399; }
</style>
