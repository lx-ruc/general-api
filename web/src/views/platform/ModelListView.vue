<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiListModels, apiCreateModel, apiUpdateModel, apiDeleteModel, type MModel } from '../../api/platform'
import { fmtPrice, fmtPrice1K } from '../../utils/format'

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
  input_price: 0, output_price: 0, cost_input_price: 0, cost_output_price: 0, status: 1, remark: '',
})

function openCreate() {
  isEdit.value = false
  Object.assign(form, { id: 0, name: '', display_name: '', vendor: '', input_price: 0, output_price: 0, cost_input_price: 0, cost_output_price: 0, status: 1, remark: '' })
  editVisible.value = true
}
function openEdit(m: MModel) {
  isEdit.value = true
  Object.assign(form, {
    id: m.id, name: m.name, display_name: m.display_name, vendor: m.vendor,
    input_price: m.input_price, output_price: m.output_price,
    cost_input_price: m.cost_input_price, cost_output_price: m.cost_output_price,
    status: m.status, remark: m.remark,
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
      cost_input_price: form.cost_input_price, cost_output_price: form.cost_output_price,
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
    `删除模型「${m.name}」会同时清理渠道能力与全部子账号授权。确定？`, '危险操作',
    { type: 'warning' },
  )
  await apiDeleteModel(m.id)
  ElMessage.success('已删除')
  load()
}

// 行内直接改价：点击单价单元格变输入框，回车/失焦即保存，值未变不请求
const editing = ref<{ id: number; field: 'input' | 'output' } | null>(null)
const editVal = ref(0)
const vFocus = { mounted: (el: HTMLElement) => { const inp = el.querySelector('input'); inp?.focus(); inp?.select() } }

function startEdit(row: MModel, field: 'input' | 'output') {
  editing.value = { id: row.id, field }
  editVal.value = field === 'input' ? row.input_price : row.output_price
}

async function savePrice(row: MModel) {
  const cur = editing.value
  if (!cur) return // change 与 blur 双触发，只处理第一次
  editing.value = null
  const orig = cur.field === 'input' ? row.input_price : row.output_price
  if (editVal.value === orig) return
  await apiUpdateModel(row.id, {
    display_name: row.display_name, vendor: row.vendor, remark: row.remark,
    input_price: cur.field === 'input' ? editVal.value : row.input_price,
    output_price: cur.field === 'output' ? editVal.value : row.output_price,
    cost_input_price: row.cost_input_price, cost_output_price: row.cost_output_price,
    status: row.status,
  })
  ElMessage.success(`${row.name} 单价已更新`)
  load()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>模型定价（单价 = 元 / M token；额度按 token 预算计（1 元 = 1,000,000 token）；点击表中单价数字可直接修改）</span>
        <el-button type="primary" @click="openCreate">新建模型</el-button>
      </div>
    </template>

    <el-table :data="list" v-loading="loading"
      empty-text="还没有模型。新建模型并定价后，才能在渠道能力与子账号授权中选用。">
      <el-table-column prop="name" label="模型名" min-width="150">
        <template #default="{ row }"><code>{{ row.name }}</code></template>
      </el-table-column>
      <el-table-column prop="display_name" label="显示名" min-width="150" />
      <el-table-column prop="vendor" label="厂商" width="90" />
      <el-table-column label="输入单价" width="170" align="right">
        <template #default="{ row }">
          <el-input-number v-if="editing && editing.id === row.id && editing.field === 'input'"
            v-model="editVal" :min="0" :step="500000" size="small" style="width: 140px"
            v-focus @change="savePrice(row)" @blur="savePrice(row)" />
          <template v-else-if="row.input_price > 0">
            <span class="price-edit num green" title="点击修改" @click="startEdit(row, 'input')">{{ fmtPrice(row.input_price, PPY) }}</span>
            <span class="dim">/M · {{ fmtPrice1K(row.input_price, PPY) }}/千</span>
          </template>
          <span v-else class="price-edit zero" title="点击定价" @click="startEdit(row, 'input')">未定价</span>
        </template>
      </el-table-column>
      <el-table-column label="输出单价" width="170" align="right">
        <template #default="{ row }">
          <el-input-number v-if="editing && editing.id === row.id && editing.field === 'output'"
            v-model="editVal" :min="0" :step="500000" size="small" style="width: 140px"
            v-focus @change="savePrice(row)" @blur="savePrice(row)" />
          <template v-else-if="row.output_price > 0">
            <span class="price-edit num green" title="点击修改" @click="startEdit(row, 'output')">{{ fmtPrice(row.output_price, PPY) }}</span>
            <span class="dim">/M · {{ fmtPrice1K(row.output_price, PPY) }}/千</span>
          </template>
          <span v-else class="price-edit zero" title="点击定价" @click="startEdit(row, 'output')">未定价</span>
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
      <el-form-item label="输入单价（元/M token）">
        <el-input-number v-model="form.input_price" :min="0" :step="500000" />
        <span class="tip">= {{ fmtPrice(form.input_price, PPY) }}/M ·{{ fmtPrice1K(form.input_price, PPY) }}/千</span>
      </el-form-item>
      <el-form-item label="输出单价（元/M token）">
        <el-input-number v-model="form.output_price" :min="0" :step="500000" />
        <span class="tip">= {{ fmtPrice(form.output_price, PPY) }}/M ·{{ fmtPrice1K(form.output_price, PPY) }}/千</span>
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
.dim { color: var(--tg-muted); font-size: 11px; }
.green { color: var(--tg-green-ink); }
.zero { color: var(--tg-amber); font-size: 11px; margin-left: 3px; }
/* 行内改价：hover 提示可点 */
.price-edit { cursor: pointer; border-bottom: 1px dashed transparent; }
.price-edit:hover { border-bottom-color: var(--tg-green-ink); }
</style>
