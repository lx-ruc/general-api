<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiListModels, apiCreateModel, apiUpdateModel, apiDeleteModel, type MModel } from '../../api/platform'
import { fmtPrice, fmtPoints } from '../../utils/format'

const PPY = 1_000_000
const list = ref<MModel[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    list.value = await apiListModels()
  } finally {
    loading.value = false
  }
}
onMounted(load)

const editVisible = ref(false)
const isEdit = ref(false)
const form = reactive({
  id: 0, name: '', display_name: '', vendor: '',
  input_price: 0, output_price: 0, status: 1, remark: '',
})

function openCreate() {
  isEdit.value = false
  Object.assign(form, { id: 0, name: '', display_name: '', vendor: '', input_price: 0, output_price: 0, status: 1, remark: '' })
  editVisible.value = true
}
function openEdit(m: MModel) {
  isEdit.value = true
  Object.assign(form, {
    id: m.id, name: m.name, display_name: m.display_name, vendor: m.vendor,
    input_price: m.input_price, output_price: m.output_price, status: m.status, remark: m.remark,
  })
  editVisible.value = true
}

async function submit() {
  if (!isEdit.value && !form.name) {
    ElMessage.warning('请填写模型名')
    return
  }
  if (isEdit.value) {
    await apiUpdateModel(form.id, {
      display_name: form.display_name, vendor: form.vendor,
      input_price: form.input_price, output_price: form.output_price,
      status: form.status, remark: form.remark,
    })
    ElMessage.success('已更新')
  } else {
    await apiCreateModel({ ...form })
    ElMessage.success('已创建')
  }
  editVisible.value = false
  load()
}

async function remove(m: MModel) {
  await ElMessageBox.confirm(
    `删除模型「${m.name}」会同时清理渠道能力与全部员工授权。确定？`, '危险操作',
    { type: 'warning' },
  )
  await apiDeleteModel(m.id)
  ElMessage.success('已删除')
  load()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>模型定价（单价 = 点/1M token，1 元 = 1,000,000 点）</span>
        <el-button type="primary" @click="openCreate">新建模型</el-button>
      </div>
    </template>

    <el-table :data="list" v-loading="loading"
      empty-text="还没有模型。新建模型并定价后，才能在渠道能力与员工授权中选用。">
      <el-table-column prop="name" label="模型名" min-width="150">
        <template #default="{ row }"><code>{{ row.name }}</code></template>
      </el-table-column>
      <el-table-column prop="display_name" label="显示名" min-width="150" />
      <el-table-column prop="vendor" label="厂商" width="90" />
      <el-table-column label="输入单价" width="120" align="right">
        <template #default="{ row }">
          <span class="num green">{{ fmtPrice(row.input_price, PPY) }}</span>
          <span v-if="row.input_price === 0" class="zero">（未定价）</span>
        </template>
      </el-table-column>
      <el-table-column label="输出单价" width="120" align="right">
        <template #default="{ row }">
          <span class="num green">{{ fmtPrice(row.output_price, PPY) }}</span>
          <span v-if="row.output_price === 0" class="zero">（未定价）</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'info'" effect="plain" size="small">
            {{ row.status === 1 ? '启用' : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
      <el-table-column label="操作" width="140" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="openEdit(row)">定价</el-button>
          <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>

  <el-dialog v-model="editVisible" :title="isEdit ? `定价：${form.name}` : '新建模型'" width="520px">
    <el-form label-width="120px">
      <el-form-item v-if="!isEdit" label="模型名" required>
        <el-input v-model="form.name" placeholder="如 deepseek-chat" />
      </el-form-item>
      <el-form-item v-else label="模型名"><el-input v-model="form.name" disabled /></el-form-item>
      <el-form-item label="显示名"><el-input v-model="form.display_name" /></el-form-item>
      <el-form-item label="厂商"><el-input v-model="form.vendor" /></el-form-item>
      <el-form-item label="输入单价（点/1M）">
        <el-input-number v-model="form.input_price" :min="0" :step="500000" />
        <span class="tip">= {{ fmtPrice(form.input_price, PPY) }}/1M 输入 token</span>
      </el-form-item>
      <el-form-item label="输出单价（点/1M）">
        <el-input-number v-model="form.output_price" :min="0" :step="500000" />
        <span class="tip">= {{ fmtPrice(form.output_price, PPY) }}/1M 输出 token</span>
      </el-form-item>
      <el-form-item label="状态">
        <el-switch v-model="form.status" :active-value="1" :inactive-value="0" active-text="启用" inactive-text="停用" />
      </el-form-item>
      <el-form-item label="备注"><el-input v-model="form.remark" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="editVisible = false">取消</el-button>
      <el-button type="primary" @click="submit">保存</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
.green { color: var(--tg-green-ink); }
.zero { color: var(--tg-amber); font-size: 11px; margin-left: 3px; }
</style>
