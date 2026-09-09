<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiListChannels, apiCreateChannel, apiUpdateChannel, apiUpdateChannelStatus,
  apiDeleteChannel, apiTestChannel, apiListModels, apiListChannelKeys, apiFetchUpstreamModels,
  apiUpdateChannelKeyStatus, apiAddChannelKeys, apiDeleteChannelKey,
  type Channel, type ChannelKeyRow,
} from '../../api/platform'
import { fmtTime } from '../../utils/format'

const list = ref<Channel[]>([])
const allModels = ref<string[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    list.value = await apiListChannels()
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  await load()
  const models = await apiListModels()
  allModels.value = models.map((m) => m.name)
})

// 新建 / 编辑
const editVisible = ref(false)
const isEdit = ref(false)
const form = reactive({
  id: 0, name: '', vendor: '', base_url: '', path: '/v1/chat/completions',
  upstream_key: '', weight: 1, priority: 0, status: 1, remark: '',
  models: [] as { model_name: string; upstream_model_name?: string }[],
})

function openCreate() {
  isEdit.value = false
  upstreamModels.value = []
  Object.assign(form, {
    id: 0, name: '', vendor: '', base_url: '', path: '/v1/chat/completions',
    upstream_key: '', weight: 1, priority: 0, status: 1, remark: '', models: [],
  })
  editVisible.value = true
}

async function openEdit(ch: Channel) {
  isEdit.value = true
  upstreamModels.value = []
  const detail = await apiListChannels() // 列表已含 models
  const full = (detail as Channel[]).find((x) => x.id === ch.id)
  Object.assign(form, {
    id: ch.id, name: ch.name, vendor: ch.vendor, base_url: ch.base_url, path: ch.path,
    upstream_key: '', weight: ch.weight, priority: ch.priority, status: ch.status,
    remark: ch.remark,
    models: (full?.models || []).map((m) => ({
      model_name: m.model_name,
      upstream_model_name: m.upstream_model_name || '',
    })),
  })
  editVisible.value = true
}

function addModelRow() {
  form.models.push({ model_name: '', upstream_model_name: '' })
}
function removeModelRow(i: number) {
  form.models.splice(i, 1)
}

// 从上游实时拉取模型列表（仅编辑模式：需已保存的 base_url 与密钥）
const upstreamModels = ref<string[]>([])
const fetchingModels = ref(false)
const selectOptions = computed(() => [...new Set([...allModels.value, ...upstreamModels.value])])

async function fetchUpstream() {
  if (!form.id) return
  fetchingModels.value = true
  try {
    const r = await apiFetchUpstreamModels(form.id)
    upstreamModels.value = r.models || []
    if (upstreamModels.value.length === 0) {
      ElMessage.warning('上游未返回任何模型')
    } else {
      ElMessage.success(`已拉取 ${upstreamModels.value.length} 个上游模型，点击模型名添加`)
    }
  } finally {
    fetchingModels.value = false
  }
}

function hasModelRow(name: string) {
  return form.models.some((m) => m.model_name === name)
}

function addUpstreamModel(name: string) {
  if (hasModelRow(name)) {
    ElMessage.info('已在列表中')
    return
  }
  form.models.push({ model_name: name, upstream_model_name: '' })
}

async function submit() {
  if (!form.name || !form.base_url || form.models.length === 0) {
    ElMessage.warning('请填写渠道名、Base URL 与至少一个模型')
    return
  }
  const payload = {
    name: form.name, vendor: form.vendor, base_url: form.base_url, path: form.path,
    upstream_key: form.upstream_key, weight: form.weight, priority: form.priority,
    status: form.status, remark: form.remark,
    models: form.models
      .filter((m) => m.model_name)
      .map((m) => ({
        model_name: m.model_name,
        upstream_model_name: m.upstream_model_name || null,
      })),
  }
  if (isEdit.value) {
    await apiUpdateChannel(form.id, payload)
    ElMessage.success('渠道已更新')
  } else {
    await apiCreateChannel(payload)
    ElMessage.success('渠道已创建')
  }
  editVisible.value = false
  load()
}

const testing = ref(0)
async function test(ch: Channel) {
  testing.value = ch.id
  try {
    const r = await apiTestChannel(ch.id)
    if (r.ok) {
      ElMessage.success(`连通正常，耗时 ${r.latency_ms}ms`)
    } else {
      ElMessageBox.alert(r.error || `HTTP ${r.status}`, '连通失败', { type: 'error' })
    }
  } finally {
    testing.value = 0
  }
  load()
}

async function toggleStatus(ch: Channel) {
  await apiUpdateChannelStatus(ch.id, ch.status === 1 ? 0 : 1)
  load()
}

async function remove(ch: Channel) {
  await ElMessageBox.confirm(`删除渠道「${ch.name}」？`, '提示', { type: 'warning' })
  await apiDeleteChannel(ch.id)
  ElMessage.success('已删除')
  load()
}

// Key 池管理（401 自动禁用后的恢复入口）
const keysVisible = ref(false)
const keysChannel = ref<Channel | null>(null)
const keyList = ref<ChannelKeyRow[]>([])
const keysLoading = ref(false)
const keyToggling = ref(0)

async function openKeys(ch: Channel) {
  keysChannel.value = ch
  keysVisible.value = true
  keysLoading.value = true
  try {
    keyList.value = await apiListChannelKeys(ch.id)
  } finally {
    keysLoading.value = false
  }
}

async function toggleKey(row: ChannelKeyRow) {
  if (!keysChannel.value) return
  keyToggling.value = row.id
  try {
    await apiUpdateChannelKeyStatus(keysChannel.value.id, row.id, row.status === 1 ? 0 : 1)
    keyList.value = await apiListChannelKeys(keysChannel.value.id)
    load()
  } finally {
    keyToggling.value = 0
  }
}

// 新增 Key（单个 / 批量共用一个输入：单个=单行密码框，批量=多行文本，均带统一权重）
const addVisible = ref(false)
const addMode = ref<'single' | 'batch'>('single')
const addKeyText = ref('')
const addWeight = ref(1)
const adding = ref(false)
const keyDeleting = ref(0)

function toggleAdd() {
  if (!addVisible.value) {
    addMode.value = 'single'
    addKeyText.value = ''
    addWeight.value = 1
  }
  addVisible.value = !addVisible.value
}

async function submitAddKeys() {
  if (!keysChannel.value) return
  // Key 本身不含换行/逗号，两种分隔都接受
  const keys = addKeyText.value.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean)
  if (keys.length === 0) {
    ElMessage.warning('请填写 Key')
    return
  }
  adding.value = true
  try {
    await apiAddChannelKeys(keysChannel.value.id, keys, addWeight.value)
    ElMessage.success(`已新增 ${keys.length} 把 Key`)
    addVisible.value = false
    keyList.value = await apiListChannelKeys(keysChannel.value.id)
    load()
  } finally {
    adding.value = false
  }
}

async function removeKey(row: ChannelKeyRow) {
  if (!keysChannel.value) return
  await ElMessageBox.confirm(
    `删除 Key ${row.key_masked}？删除后不可恢复（只是暂时不用请选「禁用」）。`,
    '提示', { type: 'warning' },
  )
  keyDeleting.value = row.id
  try {
    await apiDeleteChannelKey(keysChannel.value.id, row.id)
    keyList.value = await apiListChannelKeys(keysChannel.value.id)
    load()
  } finally {
    keyDeleting.value = 0
  }
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>渠道管理（OpenAI 兼容厂商）</span>
        <el-button type="primary" @click="openCreate">新建渠道</el-button>
      </div>
    </template>

    <el-table :data="list" v-loading="loading"
      empty-text="还没有渠道。新建渠道（base_url + 上游密钥 + 模型列表）即可开始转发，预置的 DeepSeek/智谱/通义填入密钥后启用。">
      <el-table-column prop="name" label="渠道" min-width="120" />
      <el-table-column prop="vendor" label="厂商" width="90" />
      <el-table-column prop="base_url" label="Base URL" min-width="220" show-overflow-tooltip />
      <el-table-column label="模型" min-width="160">
        <template #default="{ row }">
          <code v-for="m in row.models" :key="m.model_name" class="model-chip">
            {{ m.model_name }}
          </code>
        </template>
      </el-table-column>
      <el-table-column label="密钥" width="110" align="center">
        <template #default="{ row }">
          <span v-if="row.has_key" class="key-ok">
            ● {{ row.key_count || 1 }} 把<span v-if="row.key_count > 1">（启 {{ row.key_active_count ?? row.key_count }}）</span>
          </span>
          <span v-else class="key-miss">● 缺失</span>
        </template>
      </el-table-column>
      <el-table-column label="优先级/权重" width="100">
        <template #default="{ row }">{{ row.priority }} / {{ row.weight }}</template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'info'" effect="plain" size="small">
            {{ row.status === 1 ? '启用' : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近测试" width="170">
        <template #default="{ row }">
          <template v-if="row.last_test_at">
            <span class="dim">{{ fmtTime(row.last_test_at) }}</span>
            <span :class="row.last_test_ok ? 'key-ok' : 'key-miss'" style="margin-left: 6px">
              {{ row.last_test_ok ? 'OK' : '失败' }}
            </span>
          </template>
          <span v-else class="dim">未测试</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="320" fixed="right">
        <template #default="{ row }">
          <!-- 五颗按钮必须一行：nowrap + 收紧按钮间距 -->
          <div class="ops">
            <el-button size="small" :loading="testing === row.id" @click="test(row)">测试</el-button>
            <el-button size="small" @click="openEdit(row)">编辑</el-button>
            <el-button size="small" @click="openKeys(row)">Key 池</el-button>
            <el-button size="small" @click="toggleStatus(row)">{{ row.status === 1 ? '停用' : '启用' }}</el-button>
            <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
          </div>
        </template>
      </el-table-column>
    </el-table>
  </el-card>

  <el-dialog v-model="editVisible" :title="isEdit ? '编辑渠道' : '新建渠道'" width="640px">
    <el-form label-width="120px">
      <el-form-item label="渠道名" required><el-input v-model="form.name" /></el-form-item>
      <el-form-item label="厂商标识"><el-input v-model="form.vendor" placeholder="deepseek / zhipu / aliyun / custom" /></el-form-item>
      <el-form-item label="Base URL" required>
        <el-input v-model="form.base_url" placeholder="https://api.deepseek.com" />
      </el-form-item>
      <el-form-item label="接口路径">
        <el-input v-model="form.path" placeholder="/v1/chat/completions" />
      </el-form-item>
      <el-form-item label="上游密钥">
        <!-- 密钥的日常增删在「Key 池」弹窗；这里只在新建时快捷填入首把（权重 1） -->
        <el-input v-if="!isEdit" v-model="form.upstream_key" show-password
          placeholder="首把密钥（可选，权重 1）；更多 Key 保存后在「Key 池」新增" />
        <div v-else class="tip">
          密钥在列表「Key 池」中管理：新增（单个 / 批量）、禁用、删除
        </div>
      </el-form-item>
      <el-row>
        <el-col :span="12">
          <el-form-item label="优先级">
            <el-input-number v-model="form.priority" />
            <div class="tip">大者优先，同级按权重负载</div>
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item label="权重">
            <el-input-number v-model="form.weight" :min="1" />
            <div class="tip">渠道之间的分流比例；每把 Key 自己的权重在「Key 池」里单独设置</div>
          </el-form-item>
        </el-col>
      </el-row>
      <el-form-item label="状态">
        <el-switch v-model="form.status" :active-value="1" :inactive-value="0" active-text="启用" inactive-text="停用" />
      </el-form-item>

      <el-divider content-position="left">模型能力（该渠道可转发的模型）</el-divider>
      <!-- 从上游实时拉取模型列表，点选即加一行（仅编辑模式：需已保存的密钥） -->
      <div v-if="isEdit" class="up-row">
        <el-button size="small" :loading="fetchingModels" @click="fetchUpstream">从上游获取模型</el-button>
        <span v-if="upstreamModels.length" class="tip">已拉取 {{ upstreamModels.length }} 个，点击模型名添加；灰色的已在列表</span>
      </div>
      <div v-if="upstreamModels.length" class="up-models">
        <code v-for="name in upstreamModels" :key="name" class="up-chip"
          :class="{ added: hasModelRow(name) }" @click="addUpstreamModel(name)">{{ name }}</code>
      </div>
      <div v-for="(m, i) in form.models" :key="i" class="model-row">
        <el-select v-model="m.model_name" filterable allow-create placeholder="模型名（对外）"
          style="width: 220px" :suffix-icon="undefined">
          <el-option v-for="name in selectOptions" :key="name" :label="name" :value="name" />
        </el-select>
        <el-input v-model="m.upstream_model_name" placeholder="上游模型名（留空=同名）" style="width: 220px; margin-left: 8px" />
        <el-button type="danger" :icon="'Delete'" circle size="small" style="margin-left: 8px" @click="removeModelRow(i)" />
      </div>
      <el-button style="margin-top: 8px" :icon="'Plus'" @click="addModelRow">添加模型</el-button>
    </el-form>
    <template #footer>
      <el-button @click="editVisible = false">取消</el-button>
      <el-button type="primary" @click="submit">保存</el-button>
    </template>
  </el-dialog>

  <el-dialog v-model="keysVisible" :title="`Key 池 — ${keysChannel?.name || ''}`" width="720px">
    <el-alert v-if="keyList.length === 0 && !keysLoading && !addVisible" type="info" :closable="false"
      :title="keysChannel?.has_key
        ? '该渠道还在用旧版单密钥（未入池）。新增 Key 后自动并入 Key 池统一管理。'
        : '该渠道还没有 Key。点击下方「新增 Key」添加第一把。'" />

    <div class="keys-toolbar">
      <el-button type="primary" size="small" @click="toggleAdd">新增 Key</el-button>
    </div>

    <!-- 新增面板：单个（单行可显示明文）/ 批量（多行或逗号分隔），统一权重 -->
    <div v-if="addVisible" class="add-panel">
      <el-radio-group v-model="addMode" size="small">
        <el-radio-button value="single">单个新增</el-radio-button>
        <el-radio-button value="batch">批量新增</el-radio-button>
      </el-radio-group>
      <el-input v-if="addMode === 'single'" v-model="addKeyText" show-password
        autocomplete="new-password" placeholder="粘贴上游 API Key（不预填任何值）" clearable />
      <el-input v-else v-model="addKeyText" type="textarea" :rows="4"
        placeholder="每行一把 Key（逗号分隔也可以），下方权重对整批生效" />
      <div class="add-weight">
        <span class="add-label">权重</span>
        <el-input-number v-model="addWeight" :min="1" size="small" />
        <span class="tip">默认 1（等概率）；配额大的账号调高，多分担请求</span>
      </div>
      <div class="add-actions">
        <el-button size="small" @click="addVisible = false">取消</el-button>
        <el-button size="small" type="primary" :loading="adding" @click="submitAddKeys">添加到池</el-button>
      </div>
    </div>

    <el-table v-if="keyList.length > 0" :data="keyList" v-loading="keysLoading" size="small">
      <el-table-column prop="id" label="#" width="60" />
      <el-table-column prop="key_masked" label="Key（打码）" min-width="170">
        <template #default="{ row }"><code>{{ row.key_masked }}</code></template>
      </el-table-column>
      <el-table-column prop="weight" label="权重" width="70" align="center" />
      <el-table-column label="状态" width="80" align="center">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'danger'" effect="plain" size="small">
            {{ row.status === 1 ? '启用' : '禁用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="remark" label="备注" min-width="140" show-overflow-tooltip />
      <el-table-column label="操作" width="150">
        <template #default="{ row }">
          <el-button size="small" :loading="keyToggling === row.id" @click="toggleKey(row)">
            {{ row.status === 1 ? '禁用' : '启用' }}
          </el-button>
          <el-button size="small" type="danger" :loading="keyDeleting === row.id" @click="removeKey(row)">
            删除
          </el-button>
        </template>
      </el-table-column>
    </el-table>

    <div class="tip keys-tip">
      权重 = 同渠道内各把 Key 分摊请求的比例（3:1 即平均每 4 次请求各担 3 次与 1 次），与渠道间的优先级/权重无关；
      上游 401/403 自动禁用对应 Key（可在此恢复），429 冷却到期自动恢复。
    </div>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.tip { font-size: 12px; color: #909399; }
/* Key 池弹窗：顶部操作条 / 新增面板 / 底部说明 */
.keys-toolbar { margin-bottom: 10px; }
.add-panel {
  display: flex; flex-direction: column; gap: 10px;
  border: 1px solid var(--el-border-color-lighter); border-radius: 6px;
  padding: 14px; margin-bottom: 12px; background: var(--el-fill-color-blank);
}
.add-weight { display: flex; align-items: center; gap: 8px; }
.add-label { font-size: 13px; }
.add-actions { display: flex; justify-content: flex-end; }
.keys-tip { margin-top: 10px; line-height: 1.7; }
.model-row { display: flex; align-items: center; margin-bottom: 8px; }
/* 操作列五颗按钮一行 */
.ops { white-space: nowrap; }
.ops :deep(.el-button + .el-button) { margin-left: 8px; }
.dim { color: var(--tg-muted); font-size: 12px; }
.key-ok { color: var(--tg-green-ink); font-size: 12px; }
.key-miss { color: var(--tg-red); font-size: 12px; }
.model-chip {
  display: inline-block; margin: 1px 4px 1px 0; padding: 1px 7px;
  background: var(--tg-green-wash); border-radius: 4px;
  font-size: 11.5px; color: var(--tg-green-ink);
}
/* 从上游拉取的模型候选：可点选添加，已添加的置灰 */
.up-row { margin-bottom: 8px; display: flex; align-items: center; gap: 10px; }
.up-models {
  display: flex; flex-wrap: wrap; gap: 6px;
  max-height: 132px; overflow-y: auto;
  padding: 8px; margin-bottom: 10px;
  border: 1px dashed var(--el-border-color-lighter); border-radius: 6px;
}
.up-chip {
  cursor: pointer; padding: 2px 8px; border-radius: 4px;
  background: var(--el-fill-color-light); font-size: 11.5px;
}
.up-chip:hover { background: var(--tg-green-wash); color: var(--tg-green-ink); }
.up-chip.added { opacity: 0.45; cursor: default; }
</style>
