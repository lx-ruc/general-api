<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiMyRequests, apiCreateRequest, apiMyModels, apiAvailableModels, type AvailableModel } from '../../api/member'
import { fmtQuota, fmtTime } from '../../utils/format'

const me = ref<any>(null)
const list = ref<any[]>([])
// 申请额度按 M tokens 录入（1M = 1,000,000 点），提交前换算成点
const M = 1_000_000
const form = reactive({ amount: 1, reason: '' })
const submitting = ref(false)

// ---- 模型申请 ----
const activeTab = ref<'quota' | 'model'>('quota')
const available = ref<AvailableModel[]>([])
const modelForm = reactive({ models: [] as string[], reason: '' })
// 未授权的模型才可勾选；已授权的仅展示（后端提交时也会剔除）
const selectable = computed(() => available.value.filter((m) => !m.granted))
const grantedModels = computed(() => available.value.filter((m) => m.granted))

async function load() {
  me.value = await apiMyModels()
  list.value = await apiMyRequests()
  available.value = await apiAvailableModels()
}
onMounted(load)

async function submit() {
  if (!form.amount || form.amount <= 0) {
    ElMessage.warning('请填写申请额度（M tokens）')
    return
  }
  submitting.value = true
  try {
    await apiCreateRequest({ amount: Math.round(form.amount * M), reason: form.reason })
    ElMessage.success('申请已提交，等待客户管理员审批')
    form.amount = 1
    form.reason = ''
    load()
  } finally {
    submitting.value = false
  }
}

async function submitModels() {
  if (modelForm.models.length === 0) {
    ElMessage.warning('请勾选要申请的模型')
    return
  }
  submitting.value = true
  try {
    await apiCreateRequest({ models: [...modelForm.models], reason: modelForm.reason })
    ElMessage.success('申请已提交，等待客户管理员审批')
    modelForm.models = []
    modelForm.reason = ''
    load()
  } finally {
    submitting.value = false
  }
}

const statusType = (s: string) => (s === 'pending' ? 'warning' : s === 'approved' ? 'success' : 'danger')
const statusName = (s: string) => ({ pending: '待审批', approved: '已批准', rejected: '已驳回' }[s] || s)
const isModel = (row: any) => row.kind === 'model'
</script>

<template>
  <div v-if="me">
    <el-card shadow="never" style="margin-bottom: 16px">
      <template #header>我的额度</template>
      <el-descriptions :column="3" border>
        <el-descriptions-item label="额度上限">
          {{ me.quota_limit == null ? '不限额' : fmtQuota(me.quota_limit) }}
        </el-descriptions-item>
        <el-descriptions-item label="已消耗">{{ fmtQuota(me.quota_used) }}</el-descriptions-item>
        <el-descriptions-item label="剩余">
          <span v-if="me.quota_limit == null" style="color: #67c23a">不限</span>
          <span v-else :style="{ color: me.quota_limit - me.quota_used > 0 ? '#67c23a' : '#f56c6c', fontWeight: 600 }">
            {{ fmtQuota(me.quota_limit - me.quota_used) }}
          </span>
        </el-descriptions-item>
      </el-descriptions>

      <el-divider content-position="left">发起申请</el-divider>
      <el-tabs v-model="activeTab">
        <el-tab-pane label="额度申请" name="quota">
          <el-form inline>
            <el-form-item label="申请额度（M tokens）">
              <el-input-number v-model="form.amount" :min="0.1" :step="1" />
              <span class="hint">= {{ fmtQuota(Math.round(form.amount * M)) }}</span>
            </el-form-item>
            <el-form-item label="理由">
              <el-input v-model="form.reason" placeholder="如：XX 项目联调需要" style="width: 260px" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" :loading="submitting" @click="submit">提交申请</el-button>
            </el-form-item>
          </el-form>
        </el-tab-pane>

        <el-tab-pane label="模型申请" name="model">
          <p v-if="available.length === 0" class="hint">平台暂无可用模型</p>
          <template v-else>
            <el-checkbox-group v-model="modelForm.models">
              <div v-for="m in selectable" :key="m.name" class="model-row">
                <el-checkbox :value="m.name">
                  <b>{{ m.name }}</b>
                  <span v-if="m.display_name" class="hint">（{{ m.display_name }}）</span>
                </el-checkbox>
              </div>
            </el-checkbox-group>
            <p v-if="selectable.length === 0" class="hint">所有已上线的模型均已授权给你，无需申请</p>
            <div v-if="grantedModels.length > 0" class="hint granted-block">
              已授权：<el-tag v-for="m in grantedModels" :key="m.name" size="small" style="margin-right: 4px">{{ m.name }}</el-tag>
            </div>
            <el-form inline style="margin-top: 8px">
              <el-form-item label="申请说明">
                <el-input v-model="modelForm.reason" placeholder="如：新项目需要接入 glm-5.2" style="width: 320px" />
              </el-form-item>
              <el-form-item>
                <el-button type="primary" :loading="submitting" :disabled="selectable.length === 0" @click="submitModels">
                  提交申请
                </el-button>
              </el-form-item>
            </el-form>
          </template>
        </el-tab-pane>
      </el-tabs>
    </el-card>

    <el-card shadow="never">
      <template #header>申请记录</template>
      <el-table :data="list">
        <el-table-column prop="id" label="#" width="60" />
        <el-table-column label="类型" width="90">
          <template #default="{ row }">
            <el-tag :type="isModel(row) ? 'primary' : 'info'" effect="plain">{{ isModel(row) ? '模型' : '额度' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="申请内容" min-width="200">
          <template #default="{ row }">
            <template v-if="isModel(row)">
              <el-tag v-for="n in row.model_names.split(',')" :key="n" size="small" style="margin-right: 4px">{{ n }}</el-tag>
            </template>
            <template v-else>{{ fmtQuota(row.amount) }}</template>
          </template>
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
.hint { margin-left: 8px; font-size: 12px; color: #909399; }
.model-row { line-height: 28px; }
.granted-block { margin: 8px 0 0; line-height: 24px; }
</style>
