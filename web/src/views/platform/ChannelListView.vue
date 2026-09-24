<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiListChannels, apiCreateChannel, apiUpdateChannel, apiUpdateChannelStatus,
  apiDeleteChannel, apiTestChannel, apiListModels, apiListChannelKeys, apiFetchUpstreamModels,
  apiFetchUpstreamModelsByForm, apiCreateModel, apiUpdateModel,
  apiUpdateChannelKeyStatus, apiAddChannelKeys, apiDeleteChannelKey, apiClearChannelKeyCooldown,
  type Channel, type ChannelKeyRow, type MModel,
} from '../../api/platform'
import { fmtTime, fmtPrice, yuanToPoints, pointsToYuan } from '../../utils/format'
import { parseCurl } from '../../utils/curl'

const list = ref<Channel[]>([])
const allModels = ref<string[]>([])
const modelMap = ref<Record<string, MModel>>({})
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    list.value = await apiListChannels()
  } finally {
    loading.value = false
  }
}
async function loadModels() {
  const models = await apiListModels()
  modelMap.value = Object.fromEntries(models.map((m) => [m.name, m]))
  allModels.value = models.map((m) => m.name)
}
onMounted(async () => {
  await load()
  await loadModels()
})

// 一站式定价（元/M，入库换算点；1 元 = 1,000,000 点）
const PPY = 1_000_000

// 模型能力行：价格留空（null）= 不登记/不改动；填了则保存渠道时自动登记或更新定价
type ModelRow = {
  model_name: string
  upstream_model_name?: string
  input_price?: number | null
  output_price?: number | null
}

// 已登记且已有价的模型显示现价（只读）；未登记或未定价的就在这里填价
function priced(name: string): boolean {
  const m = modelMap.value[name.trim()]
  return !!m && (m.input_price > 0 || m.output_price > 0)
}

// 渠道列表模型 chip 上的单价：¥输入/¥输出（元/M，去尾零）；未定价的标黄提醒
function chipPrice(name: string): string {
  const m = modelMap.value[name]
  if (!m) return ''
  const f = (p: number) => `¥${(p / PPY).toFixed(2).replace(/\.?0+$/, '')}`
  return `${f(m.input_price)}/${f(m.output_price)}`
}

function chipTitle(name: string): string {
  const m = modelMap.value[name]
  if (!m) return `${name}：未登记定价（无法授权调用）`
  if (m.input_price === 0 && m.output_price === 0) return `${name}：未定价（0 元计费，可在操作列「定价」里补）`
  const hit = m.input_cache_hit_price > 0 ? ` / 命中 ${fmtPrice(m.input_cache_hit_price, PPY)}` : ''
  return `${name}：输入 ${fmtPrice(m.input_price, PPY)}${hit} / 输出 ${fmtPrice(m.output_price, PPY)} 元/M`
}

// 新建 / 编辑
const editVisible = ref(false)
const isEdit = ref(false)
// 「没配密钥就没有模型」：编辑中的渠道是否已有密钥（决定能否配模型）
const editHasKey = ref(false)
const form = reactive({
  id: 0, name: '', vendor: '', base_url: '', path: '/v1/chat/completions',
  upstream_key: '', weight: 1, priority: 0, status: 1, remark: '',
  models: [] as ModelRow[],
})

function openCreate() {
  isEdit.value = false
  upstreamModels.value = []
  curlText.value = ''
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
  editHasKey.value = !!full?.has_key
  Object.assign(form, {
    id: ch.id, name: ch.name, vendor: ch.vendor, base_url: ch.base_url, path: ch.path,
    upstream_key: '', weight: ch.weight, priority: ch.priority, status: ch.status,
    remark: ch.remark,
    models: (full?.models || []).map((m) => ({
      model_name: m.model_name,
      upstream_model_name: m.upstream_model_name || '',
      input_price: null, // 已登记模型的现价只读展示；只有填了新值才会更新
      output_price: null,
    })),
  })
  editVisible.value = true
}

function addModelRow() {
  form.models.push({ model_name: '', upstream_model_name: '', input_price: null, output_price: null })
}
function removeModelRow(i: number) {
  form.models.splice(i, 1)
}

// 从上游实时拉取模型列表：编辑模式用已保存渠道的密钥；新建模式直接按表单里的
// base_url / 路径 / 密钥拉（渠道尚未保存），避免手填预置清单里已下架的模型名
const upstreamModels = ref<string[]>([])
const fetchingModels = ref(false)
// 拉取过上游清单后，下拉只给上游现存模型——「模型定价」登记表里的已下架名不再混进来；
// 未拉取时退回登记表清单（此时仍可手输任意名）
const selectOptions = computed(() =>
  upstreamModels.value.length > 0 ? upstreamModels.value : allModels.value,
)

// 上游已下架判定：拉取后，行的对外名与映射名都不在上游清单 → 标记并可一键清理。
// 配了映射（对外名 ≠ 上游名）的行看映射名是否在清单里，避免误伤
function deprecatedUpstream(m: ModelRow): boolean {
  if (upstreamModels.value.length === 0 || !m.model_name) return false
  const up = (m.upstream_model_name || '').trim()
  if (up && upstreamModels.value.includes(up)) return false
  return !upstreamModels.value.includes(m.model_name.trim())
}
const deprecatedCount = computed(() => form.models.filter(deprecatedUpstream).length)

function removeDeprecated() {
  form.models = form.models.filter((m) => !deprecatedUpstream(m))
}

async function fetchUpstream() {
  fetchingModels.value = true
  try {
    let r: { models?: string[] } | undefined
    if (isEdit.value) {
      r = await apiFetchUpstreamModels(form.id)
    } else {
      if (!form.base_url || !form.upstream_key) {
        ElMessage.warning('请先填写 Base URL 与上游密钥，再从上游获取模型')
        return
      }
      r = await apiFetchUpstreamModelsByForm({
        base_url: form.base_url, path: form.path, upstream_key: form.upstream_key,
      })
    }
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
  form.models.push({ model_name: name, upstream_model_name: '', input_price: null, output_price: null })
}

// 粘贴 curl 快速接入（仅新建模式）：本地解析出 base_url / 端点 / 密钥 / 模型名预填表单
const curlText = ref('')

function applyCurl(): void {
  const parsed = parseCurl(curlText.value)
  if (!parsed) {
    ElMessage.warning('未识别出 URL：请粘贴厂商控制台复制的完整 curl 示例，或只贴接口地址')
    return
  }
  form.base_url = parsed.baseUrl
  if (parsed.path) form.path = parsed.path
  if (!form.vendor && parsed.vendor) form.vendor = parsed.vendor
  if (!form.name) form.name = parsed.host
  if (parsed.apiKey) form.upstream_key = parsed.apiKey
  if (parsed.model && !hasModelRow(parsed.model)) {
    form.models.push({ model_name: parsed.model, upstream_model_name: '', input_price: null, output_price: null })
    if (!allModels.value.includes(parsed.model)) {
      ElMessage.warning(`模型「${parsed.model}」尚未登记定价：可直接在下方模型行填单价，保存渠道时自动登记`)
    }
  }
  ElMessage.success('已解析并填充表单')
  for (const w of parsed.warnings) ElMessage.warning(w)
}

function onCurlPaste(e: ClipboardEvent): void {
  const text = e.clipboardData?.getData('text') ?? ''
  if (!text.trim()) return
  curlText.value = text
  applyCurl()
}

async function submit() {
  if (!form.name || !form.base_url || form.models.length === 0) {
    ElMessage.warning('请填写渠道名、Base URL 与至少一个模型')
    return
  }
  // 没配密钥就没有模型：模型必须来自「配 key → 从上游获取」
  if (!isEdit.value && !form.upstream_key) {
    ElMessage.warning('请先填上游密钥：配好密钥后从上游获取模型，无密钥渠道不配置模型')
    return
  }
  if (isEdit.value && !editHasKey.value) {
    ElMessage.warning('该渠道还没有密钥：请先到「Key 池」添加密钥，再从上游获取模型并保存')
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
        // 元/M → 点/M；null=不登记/不改动（0 是合法的免费价，照发）
        input_price: m.input_price == null ? null : yuanToPoints(m.input_price, PPY),
        output_price: m.output_price == null ? null : yuanToPoints(m.output_price, PPY),
      })),
  }
  if (isEdit.value) {
    await apiUpdateChannel(form.id, payload)
    ElMessage.success('渠道已更新')
  } else {
    const res = await apiCreateChannel(payload)
    ElMessage.success('渠道已创建')
    autoTestCreated(res?.id)
  }
  await loadModels() // 渠道侧登记的定价要反映到模型行「已定价」判断里
  // 保存后兜底提醒：仍未登记且这次也没填价的模型（定价页不出现、无法授权计费）
  const unregistered = [...new Set(
    form.models
      .filter((m) => {
        const n = m.model_name.trim()
        return n && !allModels.value.includes(n) && m.input_price == null && m.output_price == null
      })
      .map((m) => m.model_name.trim()),
  )]
  if (unregistered.length > 0) {
    ElMessage.warning(`模型 ${unregistered.join('、')} 尚未定价：无法授权调用。编辑渠道填单价或到「模型定价」登记（0 元=免费）`)
  }
  editVisible.value = false
  load()
}

// 创建成功后自动探活一次：粘贴接入时免去手动点「测试」；失败不影响创建结果
function autoTestCreated(id: number | undefined): void {
  if (!id) return
  apiTestChannel(id)
    .then((r) => {
      if (r.ok) {
        ElMessage.success(`渠道自动测试通过，耗时 ${r.latency_ms}ms`)
      } else {
        ElMessageBox.alert(r.error || `HTTP ${r.status}`, '自动测试未通过', { type: 'error' })
      }
    })
    .catch(() => { /* 探活请求本身的异常由 http 拦截器统一提示 */ })
}

const testing = ref(0)
async function test(ch: Channel) {
  testing.value = ch.id
  try {
    const r = await apiTestChannel(ch.id)
    if (r.ok) {
      // 探测成功即已清除该 Key 的冷却（后端行为）：顺手刷新 Key 池角标
      ElMessage.success(`连通正常，耗时 ${r.latency_ms}ms${r.key ? `（${r.key}）` : ''}`)
      if (keysVisible.value && keysChannel.value?.id === ch.id) {
        keyList.value = await apiListChannelKeys(ch.id)
      }
    } else if (r.quota_exhausted) {
      // 配额耗尽 ≠ 渠道故障：渠道与其它 Key 不受影响，明确区分展示
      ElMessageBox.alert(r.error || '上游配额耗尽', '上游配额耗尽（厂商侧限额）', { type: 'warning' })
      load()
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
const cooldownClearing = ref(0)

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

// 配额冷却汇总：进弹窗第一眼就能看到有几把 Key 因厂商侧限额被冷却（不用逐行找）
const quotaCoolingCount = computed(() => keyList.value.filter((k) => k.quota_cooling).length)

// 冷却行整行高亮（配额=琥珀、普通限流=浅灰），冷却状态不被淹没在表格里
function keyRowClass({ row }: { row: ChannelKeyRow }): string {
  if (row.quota_cooling) return 'quota-cooling-row'
  if (row.cooling) return 'cooling-row'
  return ''
}

// 清除冷却：厂商侧限额恢复/排障后立即把 Key 放回轮询池，不必等指数退避到期
async function clearCooldown(row: ChannelKeyRow) {
  if (!keysChannel.value) return
  cooldownClearing.value = row.id
  try {
    await apiClearChannelKeyCooldown(keysChannel.value.id, row.id)
    ElMessage.success('已清除冷却，该 Key 立即回到轮询池')
    keyList.value = await apiListChannelKeys(keysChannel.value.id)
  } finally {
    cooldownClearing.value = 0
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

// 渠道级定价：从列表「定价」进来，看该渠道的全部模型并就地填价（元/M，入库换算点）。
// 未登记的模型填价即建 models 行；已登记的改价即更新，留空字段保持不变
const priceVisible = ref(false)
const priceChannel = ref<Channel | null>(null)
const priceRows = ref<PriceRow[]>([])
const priceSaving = ref(false)

type PriceRow = {
  name: string
  registered: boolean
  input_price: number | null   // 元/M；null=保持不变（未登记模型=不建）
  output_price: number | null
  cache_price: number | null   // 缓存命中输入单价（元/M）；0=同输入价
}

function openPricing(ch: Channel) {
  priceChannel.value = ch
  priceRows.value = (ch.models || []).map((ab) => {
    const m = modelMap.value[ab.model_name]
    return {
      name: ab.model_name,
      registered: !!m,
      input_price: m ? pointsToYuan(m.input_price, PPY) : null,
      output_price: m ? pointsToYuan(m.output_price, PPY) : null,
      cache_price: m ? pointsToYuan(m.input_cache_hit_price, PPY) : 0,
    }
  })
  priceVisible.value = true
}

async function savePricing() {
  if (!priceChannel.value) return
  priceSaving.value = true
  try {
    let touched = 0
    for (const r of priceRows.value) {
      const m = modelMap.value[r.name]
      if (m) {
        // 已登记：只提交变化的字段，其余沿用现值（UpdateModel 未传字段不动）
        const newIn = r.input_price == null ? m.input_price : yuanToPoints(r.input_price, PPY)
        const newOut = r.output_price == null ? m.output_price : yuanToPoints(r.output_price, PPY)
        const newHit = r.cache_price == null ? m.input_cache_hit_price : yuanToPoints(r.cache_price, PPY)
        if (newIn === m.input_price && newOut === m.output_price && newHit === m.input_cache_hit_price) continue
        await apiUpdateModel(m.id, {
          display_name: m.display_name, vendor: m.vendor, remark: m.remark,
          input_price: newIn, output_price: newOut, input_cache_hit_price: newHit,
          cost_input_price: m.cost_input_price, cost_output_price: m.cost_output_price,
          status: m.status,
        })
        touched++
      } else if (r.input_price != null || r.output_price != null) {
        await apiCreateModel({
          name: r.name, display_name: '', vendor: priceChannel.value.vendor,
          input_price: yuanToPoints(r.input_price ?? 0, PPY),
          output_price: yuanToPoints(r.output_price ?? 0, PPY),
          input_cache_hit_price: yuanToPoints(r.cache_price ?? 0, PPY),
          cost_input_price: 0, cost_output_price: 0, status: 1, remark: '',
        })
        touched++
      }
    }
    if (touched > 0) {
      ElMessage.success(`已保存 ${touched} 个模型的定价`)
    } else {
      ElMessage.info('没有变动')
    }
    await loadModels()
    priceVisible.value = false
  } finally {
    priceSaving.value = false
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
      <el-table-column prop="name" label="渠道" width="140" />
      <el-table-column prop="vendor" label="厂商" width="90" />
      <el-table-column label="模型（含单价 元/M）" min-width="200">
        <template #default="{ row }">
          <code v-for="m in row.models" :key="m.model_name" class="model-chip"
            :class="{ 'chip-unpriced': !priced(m.model_name) }" :title="chipTitle(m.model_name)">
            {{ m.model_name }}
            <span v-if="priced(m.model_name)" class="chip-price">{{ chipPrice(m.model_name) }}</span>
            <span v-else class="chip-zero">未定价</span>
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
      <el-table-column label="操作" width="380" fixed="right">
        <template #default="{ row }">
          <!-- 六颗按钮必须一行：nowrap + 收紧按钮间距 -->
          <div class="ops">
            <el-button size="small" :loading="testing === row.id" @click="test(row)">测试</el-button>
            <el-button size="small" @click="openEdit(row)">编辑</el-button>
            <el-button size="small" @click="openPricing(row)">定价</el-button>
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
      <!-- 粘贴厂商 curl 示例快速填表（仅新建模式）；解析纯前端完成，密钥只进表单 -->
      <el-form-item v-if="!isEdit" label="快速接入">
        <div class="curl-box" @paste="onCurlPaste">
          <el-input v-model="curlText" type="textarea" :rows="5" resize="none"
            placeholder="粘贴厂商控制台复制的 curl 示例（自动解析出 Base URL、密钥、模型名并填充下方表单）；只贴接口地址也可以" />
          <div class="curl-actions">
            <el-button size="small" @click="applyCurl">解析并填充</el-button>
            <span class="tip">粘贴后自动解析；密钥只存入本表单（落库加密），不会外发</span>
          </div>
        </div>
      </el-form-item>
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
          <span v-if="!editHasKey" class="key-miss">该渠道还没有密钥：先到「Key 池」添加，才能配置模型</span>
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
      <!-- 从上游实时拉取模型列表，点选即加一行（编辑用已存密钥；新建用表单里填的密钥） -->
      <div class="up-row">
        <el-button size="small" :loading="fetchingModels" @click="fetchUpstream">从上游获取模型</el-button>
        <el-button v-if="deprecatedCount > 0" size="small" type="warning" plain @click="removeDeprecated">
          移除已下架（{{ deprecatedCount }}）
        </el-button>
        <span v-if="!isEdit" class="tip">按表单里的 Base URL 与密钥拉取（需先填写）</span>
        <span v-if="upstreamModels.length" class="tip">
          已拉取 {{ upstreamModels.length }} 个现存模型，点击添加；下拉与列表均以上游为准，标记「上游已下架」的官方已停售
        </span>
      </div>
      <div v-if="upstreamModels.length" class="up-models">
        <code v-for="name in upstreamModels" :key="name" class="up-chip"
          :class="{ added: hasModelRow(name) }" @click="addUpstreamModel(name)">{{ name }}</code>
      </div>
      <div v-for="(m, i) in form.models" :key="i" class="model-card">
        <div class="model-row">
          <el-select v-model="m.model_name" filterable allow-create placeholder="模型名（对外）"
            style="width: 220px" :suffix-icon="undefined">
            <el-option v-for="name in selectOptions" :key="name" :label="name" :value="name" />
          </el-select>
          <el-input v-model="m.upstream_model_name" placeholder="上游模型名（留空=同名）" style="width: 220px; margin-left: 8px" />
          <el-tag v-if="deprecatedUpstream(m)" type="warning" effect="plain" size="small" style="margin-left: 8px">
            上游已下架
          </el-tag>
          <el-button type="danger" :icon="'Delete'" circle size="small" style="margin-left: 8px" @click="removeModelRow(i)" />
        </div>
        <!-- 一站式定价：已定价的模型只读显示现价；未定价的可当场填价，保存渠道时自动登记（元/M） -->
        <div v-if="m.model_name && priced(m.model_name)" class="price-row priced">
          已定价 {{ fmtPrice(modelMap[m.model_name.trim()].input_price, PPY) }} /
          {{ fmtPrice(modelMap[m.model_name.trim()].output_price, PPY) }} 元/M（如需调整请到「模型定价」）
        </div>
        <div v-else-if="m.model_name" class="price-row">
          <span class="price-label">单价（元/M）</span>
          <el-input-number v-model="m.input_price" :min="0" :step="1" :precision="2" size="small"
            style="width: 116px" placeholder="输入" controls-position="right" />
          <span class="dim">输入 ·</span>
          <el-input-number v-model="m.output_price" :min="0" :step="1" :precision="2" size="small"
            style="width: 116px" placeholder="输出" controls-position="right" />
          <span class="dim">输出 · 留空=暂不登记（无法授权调用）</span>
        </div>
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

    <el-alert v-if="quotaCoolingCount > 0" type="warning" :closable="false" class="keys-alert"
      :title="`有 ${quotaCoolingCount} 把 Key 因厂商侧配额耗尽处于冷却（该行已标黄）：限额恢复后点行内「清除冷却」立即复用，无需等自动到期`" />

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

    <el-table v-if="keyList.length > 0" :data="keyList" v-loading="keysLoading" size="small"
      :row-class-name="keyRowClass">
      <el-table-column prop="id" label="#" width="44" />
      <el-table-column prop="key_masked" label="Key（打码）" min-width="140">
        <template #default="{ row }"><code>{{ row.key_masked }}</code></template>
      </el-table-column>
      <el-table-column prop="weight" label="权重" width="56" align="center" />
      <el-table-column label="状态" width="66" align="center">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'danger'" effect="plain" size="small">
            {{ row.status === 1 ? '启用' : '禁用' }}
          </el-tag>
        </template>
      </el-table-column>
      <!-- 冷却状态与解除动作放同一列：不用横向滚动到操作列就能看到并点「清除冷却」 -->
      <el-table-column label="冷却 / 解除" min-width="170">
        <template #default="{ row }">
          <div v-if="row.cooling" class="cool-cell">
            <el-tooltip v-if="row.quota_cooling" content="上游厂商侧配额耗尽（如火山限额），冷却期内该渠道全部 Key 耗尽时回 402；冷却到期由真实流量自动再探测，限额恢复后点右侧「清除冷却」立即复用"
              placement="top">
              <el-tag type="warning" effect="light" size="small">配额冷却</el-tag>
            </el-tooltip>
            <el-tag v-else type="info" effect="plain" size="small">冷却中</el-tag>
            <el-button link type="primary" size="small" class="cool-clear"
              :loading="cooldownClearing === row.id" @click="clearCooldown(row)">清除冷却</el-button>
          </div>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column prop="remark" label="备注" show-overflow-tooltip />
      <el-table-column label="操作" width="126">
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
      上游 401/403 自动禁用对应 Key（可在此恢复）；429 冷却（含厂商侧配额耗尽）按 key_cooldown 起步
      （默认 1 分钟），到期由真实流量自动再探测、无需人工干预——配额恢复后下一笔请求即成功，
      等不及的话点「冷却 / 解除」列的「清除冷却」立即复用。Key 进入配额冷却时会站内通知系统管理员（顶栏铃铛）。
    </div>
  </el-dialog>

  <!-- 渠道级定价：该渠道的全部模型一览，未定价的当场登记、已定价的就地改价 -->
  <el-dialog v-model="priceVisible" :title="`定价 — ${priceChannel?.name || ''}`" width="680px">
    <el-alert v-if="priceRows.length === 0" type="info" :closable="false"
      title="该渠道还没有配置模型能力，先在「编辑」里添加模型。" />
    <div v-for="r in priceRows" :key="r.name" class="price-card">
      <div class="price-head">
        <code class="model-chip">{{ r.name }}</code>
        <span v-if="r.registered && modelMap[r.name] && (modelMap[r.name].input_price > 0 || modelMap[r.name].output_price > 0)"
          class="dim">现价 {{ fmtPrice(modelMap[r.name].input_price, PPY) }}
          <template v-if="modelMap[r.name].input_cache_hit_price > 0">（命中 {{ fmtPrice(modelMap[r.name].input_cache_hit_price, PPY) }}）</template>
          / {{ fmtPrice(modelMap[r.name].output_price, PPY) }} 元/M</span>
        <el-tag v-else type="warning" effect="plain" size="small">未定价</el-tag>
      </div>
      <div class="price-edit-row">
        <span class="price-label">单价（元/M）</span>
        <el-input-number v-model="r.input_price" :min="0" :step="1" :precision="2" size="small"
          style="width: 118px" placeholder="输入" controls-position="right" />
        <span class="dim">输入</span>
        <el-input-number v-model="r.cache_price" :min="0" :step="0.5" :precision="2" size="small"
          style="width: 118px" placeholder="缓存命中" controls-position="right" />
        <span class="dim">命中</span>
        <el-input-number v-model="r.output_price" :min="0" :step="1" :precision="2" size="small"
          style="width: 118px" placeholder="输出" controls-position="right" />
        <span class="dim">输出</span>
      </div>
    </div>
    <div class="tip" style="margin-top: 8px">
      按元/M token 填写，保存时自动换算成 token 点数入库；缓存命中 = 上游提示缓存命中的输入 tokens 单价（0 = 同输入价）；
      已定价模型留空 = 保持不变，未定价模型留空 = 不登记（无法授权调用，0 元=免费）。
    </div>
    <template #footer>
      <el-button @click="priceVisible = false">取消</el-button>
      <el-button type="primary" :loading="priceSaving" @click="savePricing">保存定价</el-button>
    </template>
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
.keys-alert { margin-bottom: 10px; }
/* 冷却列：角标 + 就地「清除冷却」同一行 */
.cool-cell { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.cool-clear { padding: 0; }
/* 冷却行整行底色（配额=琥珀 / 普通限流=浅灰），一眼锁定需要关注的 Key */
:deep(.el-table .quota-cooling-row) { --el-table-tr-bg-color: #fdf3e3; }
:deep(.el-table .cooling-row) { --el-table-tr-bg-color: #f5f6f7; }
.model-row { display: flex; align-items: center; }
/* 模型能力卡片：上行=名称/映射，下行=就地定价 */
.model-card {
  border: 1px solid var(--el-border-color-lighter); border-radius: 6px;
  padding: 8px 10px; margin-bottom: 8px; background: var(--el-fill-color-blank);
}
.price-row {
  display: flex; align-items: center; gap: 8px; margin-top: 8px;
}
.price-row.priced { font-size: 12px; color: var(--tg-muted); }
.price-label { font-size: 12.5px; color: var(--el-text-color-regular); }
/* 渠道级定价弹窗的模型行 */
.price-card {
  border: 1px solid var(--el-border-color-lighter); border-radius: 6px;
  padding: 8px 12px; margin-bottom: 8px;
}
.price-head { display: flex; align-items: center; gap: 10px; }
.price-edit-row { display: flex; align-items: center; gap: 8px; margin-top: 8px; }
/* 粘贴 curl 快速接入区 */
.curl-box { width: 100%; }
.curl-actions { display: flex; align-items: center; gap: 10px; margin-top: 6px; }
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
/* 渠道列表：chip 内嵌单价（绿色正常 / 黄色未定价） */
.chip-price { margin-left: 4px; opacity: 0.85; }
.chip-zero { margin-left: 4px; }
.chip-unpriced {
  background: transparent; border: 1px dashed var(--tg-amber);
  color: var(--tg-amber);
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
