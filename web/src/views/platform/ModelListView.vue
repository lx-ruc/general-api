<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiListModels, apiUpdateModel, apiDeleteModel, type MModel } from '../../api/platform'
import { fmtPrice, yuanToPoints, pointsToYuan } from '../../utils/format'

const PPY = 1_000_000
const list = ref<MModel[]>([])
const loading = ref(false)

// 只看「已接通」的模型（有启用且有密钥的渠道）；默认开，预置未接渠道的模型不干扰视线
const onlyLive = ref(true)
const liveCount = computed(() => list.value.filter((m) => (m.channel_count ?? 0) > 0).length)
const filteredList = computed(() =>
  onlyLive.value ? list.value.filter((m) => (m.channel_count ?? 0) > 0) : list.value,
)
const emptyText = computed(() =>
  onlyLive.value
    ? `没有已接通的模型（${liveCount.value}/${list.value.length} 个模型接通了启用渠道＋密钥）。取消右上角「只看已接通」可查看全部。`
    : '还没有模型。到「渠道管理」新建渠道（填密钥 → 从上游获取模型 → 保存时定价），模型会自动登记到这里。',
)

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
const form = reactive({
  id: 0, name: '', display_name: '', vendor: '',
  input_price: 0, output_price: 0, input_cache_hit_price: 0,
  cost_input_price: 0, cost_output_price: 0, status: 1, remark: '',
})

// 模型不在此手动新建：只能经「渠道管理」配密钥从上游获取后自动登记（此处只改价/状态/备注）
function openEdit(m: MModel) {
  Object.assign(form, {
    id: m.id, name: m.name, display_name: m.display_name, vendor: m.vendor,
    // 表单按元/M 编辑，入库前换算回点（1 元 = 1,000,000 点）
    input_price: pointsToYuan(m.input_price), output_price: pointsToYuan(m.output_price),
    input_cache_hit_price: pointsToYuan(m.input_cache_hit_price),
    cost_input_price: m.cost_input_price, cost_output_price: m.cost_output_price,
    status: m.status, remark: m.remark,
  })
  editVisible.value = true
}

async function submit() {
  await apiUpdateModel(form.id, {
    display_name: form.display_name, vendor: form.vendor,
    input_price: yuanToPoints(form.input_price), output_price: yuanToPoints(form.output_price),
    input_cache_hit_price: yuanToPoints(form.input_cache_hit_price),
    cost_input_price: form.cost_input_price, cost_output_price: form.cost_output_price,
    status: form.status, remark: form.remark,
  })
  ElMessage.success('已更新')
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
  editVal.value = pointsToYuan(field === 'input' ? row.input_price : row.output_price)
}

async function savePrice(row: MModel) {
  const cur = editing.value
  if (!cur) return // change 与 blur 双触发，只处理第一次
  editing.value = null
  const orig = pointsToYuan(cur.field === 'input' ? row.input_price : row.output_price)
  if (editVal.value === orig) return
  await apiUpdateModel(row.id, {
    display_name: row.display_name, vendor: row.vendor, remark: row.remark,
    input_price: cur.field === 'input' ? yuanToPoints(editVal.value) : row.input_price,
    output_price: cur.field === 'output' ? yuanToPoints(editVal.value) : row.output_price,
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
        <span>模型定价（单价按「元 / M token」填写与展示，入库自动换算成 token 点数，1 元 = 1,000,000 点；点击表中单价数字可直接修改）</span>
        <div class="header-actions">
          <el-checkbox v-model="onlyLive">只看已接通（{{ liveCount }}/{{ list.length }}）</el-checkbox>
        </div>
      </div>
    </template>

    <el-table :data="filteredList" v-loading="loading" :empty-text="emptyText">
      <el-table-column prop="name" label="模型名" min-width="150">
        <template #default="{ row }"><code>{{ row.name }}</code></template>
      </el-table-column>
      <el-table-column prop="display_name" label="显示名" min-width="150" />
      <el-table-column prop="vendor" label="厂商" width="90" />
      <el-table-column label="可用渠道" width="100" align="center">
        <template #default="{ row }">
          <el-tag v-if="(row.channel_count ?? 0) > 0" type="success" effect="plain" size="small">
            {{ row.channel_count }} 渠道
          </el-tag>
          <el-tag v-else type="info" effect="plain" size="small">未接</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="输入单价" width="170" align="right">
        <template #default="{ row }">
          <el-input-number v-if="editing && editing.id === row.id && editing.field === 'input'"
            v-model="editVal" :min="0" :step="1" :precision="2" size="small" style="width: 106px"
            v-focus @change="savePrice(row)" @blur="savePrice(row)" />
          <span v-if="editing && editing.id === row.id && editing.field === 'input'" class="dim">元/M</span>
          <template v-else-if="row.input_price > 0">
            <span class="price-edit num green" title="点击修改" @click="startEdit(row, 'input')">{{ fmtPrice(row.input_price, PPY) }}</span>
            <span class="dim">/M tokens</span>
            <span v-if="row.input_cache_hit_price > 0" class="cache-sub" title="提示缓存命中的输入单价">
              命中 {{ fmtPrice(row.input_cache_hit_price, PPY) }}
            </span>
          </template>
          <span v-else class="price-edit zero" title="点击定价" @click="startEdit(row, 'input')">未定价</span>
        </template>
      </el-table-column>
      <el-table-column label="输出单价" width="170" align="right">
        <template #default="{ row }">
          <el-input-number v-if="editing && editing.id === row.id && editing.field === 'output'"
            v-model="editVal" :min="0" :step="1" :precision="2" size="small" style="width: 106px"
            v-focus @change="savePrice(row)" @blur="savePrice(row)" />
          <span v-if="editing && editing.id === row.id && editing.field === 'output'" class="dim">元/M</span>
          <template v-else-if="row.output_price > 0">
            <span class="price-edit num green" title="点击修改" @click="startEdit(row, 'output')">{{ fmtPrice(row.output_price, PPY) }}</span>
            <span class="dim">/M tokens</span>
          </template>
          <span v-else class="price-edit zero" title="点击定价" @click="startEdit(row, 'output')">未定价</span>
        </template>
      </el-table-column>
      <el-table-column label="调用状态" width="100"
        title="模型需启用、有带密钥的启用渠道、且已定价，客户才能真正调用；未定价=0 元计费">
        <template #default="{ row }">
          <el-tag v-if="row.status !== 1" type="info" effect="plain" size="small">已停用</el-tag>
          <el-tag v-else-if="(row.channel_count ?? 0) === 0" type="warning" effect="plain" size="small">未接渠道</el-tag>
          <el-tag v-else-if="row.input_price === 0 && row.output_price === 0" type="warning" effect="plain" size="small">未定价</el-tag>
          <el-tag v-else type="success" effect="plain" size="small">可用</el-tag>
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

  <el-dialog v-model="editVisible" :title="`定价：${form.name}`" width="520px">
    <el-form label-width="120px">
      <el-form-item label="模型名"><el-input v-model="form.name" disabled /></el-form-item>
      <el-form-item label="显示名"><el-input v-model="form.display_name" /></el-form-item>
      <el-form-item label="厂商"><el-input v-model="form.vendor" /></el-form-item>
      <el-form-item label="输入单价（元/M）">
        <el-input-number v-model="form.input_price" :min="0" :step="1" :precision="4" />
        <span class="tip">= {{ yuanToPoints(form.input_price, PPY).toLocaleString('zh-CN') }} 点/M</span>
      </el-form-item>
      <el-form-item label="缓存命中单价（元/M）">
        <el-input-number v-model="form.input_cache_hit_price" :min="0" :step="0.5" :precision="4" />
        <span class="tip">提示缓存命中的输入 tokens 按此价计；0 = 同输入单价</span>
      </el-form-item>
      <el-form-item label="输出单价（元/M）">
        <el-input-number v-model="form.output_price" :min="0" :step="1" :precision="4" />
        <span class="tip">= {{ yuanToPoints(form.output_price, PPY).toLocaleString('zh-CN') }} 点/M</span>
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
.header-actions { display: flex; align-items: center; gap: 12px; }
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
.dim { color: var(--tg-muted); font-size: 11px; }
.cache-sub { display: block; font-size: 11px; color: var(--tg-amber); line-height: 1.4; }
.green { color: var(--tg-green-ink); }
.zero { color: var(--tg-amber); font-size: 11px; margin-left: 3px; }
/* 行内改价：hover 提示可点 */
.price-edit { cursor: pointer; border-bottom: 1px dashed transparent; }
.price-edit:hover { border-bottom-color: var(--tg-green-ink); }
</style>
