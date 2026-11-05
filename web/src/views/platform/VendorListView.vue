<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiListVendors, apiCreateVendor, apiUpdateVendor, apiDeleteVendor, apiTestVendor,
  apiListVendorKeys, apiAddVendorKeys, apiDeleteVendorKey, apiVendorKeyFull,
  apiUpdateVendorKeyStatus, apiClearVendorKeyCooldown,
  apiListChannels, apiCreateChannel, apiUpdateChannel, apiUpdateChannelStatus,
  apiDeleteChannel, apiFetchUpstreamModels, apiFetchUpstreamModelsByForm, apiListModels,
  apiListOrgs, apiListOrgVendorPrices, apiSetOrgVendorPrice, apiDeleteOrgVendorPrice,
  type Vendor, type ChannelKeyRow, type Channel, type ChannelTestResult, type OrgVendorPriceRow,
} from '../../api/platform'
import MaskedKey from '../../components/MaskedKey.vue'
import { fmtTime } from '../../utils/format'
import { parseCurl } from '../../utils/curl'

// 厂商管理：一行一厂商（Key 池 + 端点 + 模型概览），端点=原渠道（base_url/路径/模型映射）

const list = ref<Vendor[]>([])
const loading = ref(false)
const allModels = ref<string[]>([])

async function load() {
  loading.value = true
  try {
    list.value = await apiListVendors()
  } finally {
    loading.value = false
  }
}
async function loadModels() {
  const models = await apiListModels()
  allModels.value = models.map((m) => m.name)
}
onMounted(async () => {
  await load()
  await loadModels()
})

function deadKeys(v: Vendor): number {
  return (v.key_count || 0) - (v.key_active_count ?? (v.key_count || 0))
}

// ---- 厂商编辑 / 新建 ----
const vEditVisible = ref(false)
const vIsEdit = ref(false)
const vForm = reactive({ id: 0, name: '', display_name: '', remark: '', status: 1 })

function openVendorCreate() {
  vIsEdit.value = false
  Object.assign(vForm, { id: 0, name: '', display_name: '', remark: '', status: 1 })
  vEditVisible.value = true
}
function openVendorEdit(v: Vendor) {
  vIsEdit.value = true
  Object.assign(vForm, {
    id: v.id, name: v.name,
    display_name: v.display_name && v.display_name !== v.name ? v.display_name : '',
    remark: v.remark, status: v.status,
  })
  vEditVisible.value = true
}
async function submitVendor() {
  if (!vForm.name.trim()) {
    ElMessage.warning('请填写厂商标识')
    return
  }
  if (vIsEdit.value) {
    await apiUpdateVendor(vForm.id, {
      name: vForm.name.trim(), display_name: vForm.display_name,
      remark: vForm.remark, status: vForm.status,
    })
    ElMessage.success('厂商已更新')
  } else {
    await apiCreateVendor({
      name: vForm.name.trim(), display_name: vForm.display_name,
      remark: vForm.remark, status: vForm.status,
    })
    ElMessage.success('厂商已创建，接下来添加 Key 与端点')
  }
  vEditVisible.value = false
  load()
}
async function toggleVendor(v: Vendor) {
  await apiUpdateVendor(v.id, { status: v.status === 1 ? 0 : 1 })
  load()
}
async function removeVendor(v: Vendor) {
  await ElMessageBox.confirm(
    `删除厂商「${v.name}」？仅当其下没有端点/模型/Key/客户开通/定价时允许删除。`, '提示',
    { type: 'warning' },
  )
  await apiDeleteVendor(v.id)
  ElMessage.success('已删除')
  load()
}

// ---- 厂商价（对该客户生效于组内全部模型；模型级覆盖优先） ----
const priceVisible = ref(false)
const priceVendor = ref<Vendor | null>(null)
const priceRows = ref<OrgVendorPriceRow[]>([])
const priceLoading = ref(false)
const priceOrgs = ref<{ id: number; name: string }[]>([])
const priceAddOrg = ref<number | undefined>(undefined)
const priceSaving = ref(0)
const priceUnit = ref<'1k' | '1m'>('1m')

const priceUnitLabel = computed(() => (priceUnit.value === '1k' ? '1K' : '1M'))
const priceUnitStep = computed(() => (priceUnit.value === '1k' ? 0.001 : 0.1))
const priceUnitPrecision = computed(() => (priceUnit.value === '1k' ? 6 : 4))
// 草稿三价按当前单位编辑；入库换算回点（存储口径点/百万 token）
const drafts = reactive<Record<number, { in: number; out: number; cache: number }>>({})
const snapshots = reactive<Record<number, { in: number; out: number; cache: number }>>({})

async function openPrices(v: Vendor) {
  priceVendor.value = v
  priceUnit.value = '1m'
  priceVisible.value = true
  priceLoading.value = true
  try {
    if (priceOrgs.value.length === 0) {
      const r = await apiListOrgs({ page: 1, page_size: 1000 })
      priceOrgs.value = (r.list || []).map((o: any) => ({ id: o.id, name: o.name }))
    }
    await reloadPrices()
  } finally {
    priceLoading.value = false
  }
}
async function reloadPrices() {
  if (!priceVendor.value) return
  priceRows.value = await apiListOrgVendorPrices(priceVendor.value.id)
  for (const k of Object.keys(drafts)) delete drafts[Number(k)]
  for (const k of Object.keys(snapshots)) delete snapshots[Number(k)]
  for (const r of priceRows.value) {
    drafts[r.org_id] = { in: toDisp(r.input_price), out: toDisp(r.output_price), cache: toDisp(r.input_cache_hit_price) }
    snapshots[r.org_id] = { ...drafts[r.org_id] }
  }
}
const toDisp = (points: number) => points / 1_000_000 // 点 → 元/1M token
const fromDisp = (yuan: number) => Math.round(yuan * 1_000_000)
function priceDirty(orgId: number): boolean {
  const d = drafts[orgId], s = snapshots[orgId]
  return !!d && !!s && (d.in !== s.in || d.out !== s.out || d.cache !== s.cache)
}
function onPriceUnitChange() {
  // 单位切换：草稿与快照同乘换算，保持未保存编辑不丢
  const factor = priceUnit.value === '1k' ? 1000 : 1 / 1000
  for (const id of Object.keys(drafts)) {
    const n = Number(id)
    for (const f of ['in', 'out', 'cache'] as const) {
      drafts[n][f] = round6(drafts[n][f] * factor)
      snapshots[n][f] = round6(snapshots[n][f] * factor)
    }
  }
}
const round6 = (x: number) => Number(x.toFixed(6))
async function addPriceRow() {
  if (!priceAddOrg.value || !priceVendor.value) return
  await apiSetOrgVendorPrice(priceVendor.value.id, {
    org_id: priceAddOrg.value, input_price: 0, output_price: 0,
  })
  priceAddOrg.value = undefined
  await reloadPrices()
}
async function savePriceRow(orgId: number) {
  if (!priceVendor.value) return
  const d = drafts[orgId]
  priceSaving.value = orgId
  try {
    await apiSetOrgVendorPrice(priceVendor.value!.id, {
      org_id: orgId,
      input_price: fromDisp(d.in), output_price: fromDisp(d.out),
      input_cache_hit_price: fromDisp(d.cache),
    })
    ElMessage.success('已保存')
    await reloadPrices()
  } finally {
    priceSaving.value = 0
  }
}
async function resetPriceRow(r: OrgVendorPriceRow) {
  if (!priceVendor.value) return
  await ElMessageBox.confirm(`恢复 ${r.org_name} 的组内模型走模型级价格（模型覆盖价 > 模型默认价）？`, '恢复', { type: 'warning' })
  await apiDeleteOrgVendorPrice(priceVendor.value.id, r.org_id)
  await reloadPrices()
}
const priceCandidates = computed(() => {
  const has = new Set(priceRows.value.map((r) => r.org_id))
  return priceOrgs.value.filter((o) => !has.has(o.id))
})

const testing = ref(0)
function latencyNote(ms: number): string {
  return ms > 3000 ? '（思考型模型 3~10 秒属正常，耗时为上游生成速度）' : ''
}
async function testVendor(v: Vendor) {
  testing.value = v.id
  try {
    const r: ChannelTestResult = await apiTestVendor(v.id)
    if (r.ok) {
      ElMessage.success(`连通正常，耗时 ${r.latency_ms}ms${r.key ? `（${r.key}）` : ''}${latencyNote(r.latency_ms)}`)
    } else if ((r as any).quota_exhausted) {
      ElMessageBox.alert(r.error || '上游配额耗尽', '上游配额耗尽（厂商侧限额）', { type: 'warning' })
    } else {
      ElMessageBox.alert(r.error || `HTTP ${r.status}`, '连通失败', { type: 'error' })
    }
  } finally {
    testing.value = 0
  }
  load()
}

// ---- Key 池（行内展开卡片） ----
const keysVendor = ref<number>(0)
const keyList = ref<ChannelKeyRow[]>([])
const keysLoading = ref(false)
const keyToggling = ref(0)
const cooldownClearing = ref(0)
const addVisible = ref(false)
const addMode = ref<'single' | 'batch'>('single')
const addKeyText = ref('')
const addWeight = ref(1)
const adding = ref(false)
const keyDeleting = ref(0)

async function openKeys(v: Vendor) {
  if (keysVendor.value === v.id) {
    keysVendor.value = 0
    return
  }
  keysVendor.value = v.id
  keysLoading.value = true
  addVisible.value = false
  try {
    keyList.value = await apiListVendorKeys(v.id)
  } finally {
    keysLoading.value = false
  }
}
async function toggleKey(row: ChannelKeyRow) {
  keyToggling.value = row.id
  try {
    await apiUpdateVendorKeyStatus(keysVendor.value, row.id, row.status === 1 ? 0 : 1)
    keyList.value = await apiListVendorKeys(keysVendor.value)
    load()
  } finally {
    keyToggling.value = 0
  }
}
const quotaCoolingCount = computed(() => keyList.value.filter((k) => k.quota_cooling).length)
const disabledKeyCount = computed(() => keyList.value.filter((k) => k.status === 0).length)
function keyRowClass({ row }: { row: ChannelKeyRow }): string {
  if (row.status === 0) return 'key-dead-row'
  if (row.quota_cooling) return 'quota-cooling-row'
  if (row.cooling) return 'cooling-row'
  return ''
}
async function clearCooldown(row: ChannelKeyRow) {
  cooldownClearing.value = row.id
  try {
    await apiClearVendorKeyCooldown(keysVendor.value, row.id)
    ElMessage.success('已清除冷却，该 Key 立即回到轮询池')
    keyList.value = await apiListVendorKeys(keysVendor.value)
    load()
  } finally {
    cooldownClearing.value = 0
  }
}
function toggleAdd() {
  if (!addVisible.value) {
    addMode.value = 'single'
    addKeyText.value = ''
    addWeight.value = 1
  }
  addVisible.value = !addVisible.value
}
async function submitAddKeys() {
  const keys = addKeyText.value.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean)
  if (keys.length === 0) {
    ElMessage.warning('请填写 Key')
    return
  }
  adding.value = true
  try {
    await apiAddVendorKeys(keysVendor.value, keys, addWeight.value)
    ElMessage.success(`已新增 ${keys.length} 把 Key`)
    addVisible.value = false
    keyList.value = await apiListVendorKeys(keysVendor.value)
    load()
  } finally {
    adding.value = false
  }
}
async function removeKey(row: ChannelKeyRow) {
  await ElMessageBox.confirm(
    `删除 Key ${row.key_masked}？删除后不可恢复（只是暂时不用请选「禁用」）。`,
    '提示', { type: 'warning' },
  )
  keyDeleting.value = row.id
  try {
    await apiDeleteVendorKey(keysVendor.value, row.id)
    keyList.value = await apiListVendorKeys(keysVendor.value)
    load()
  } finally {
    keyDeleting.value = 0
  }
}

// ---- 端点（渠道）编辑 / 新建：Key 属厂商池，表单不再有密钥字段 ----
type ModelRow = { model_name: string; upstream_model_name?: string }

const epVisible = ref(false)
const epIsEdit = ref(false)
const epVendorId = ref(0)
const epForm = reactive({
  id: 0, name: '', base_url: '', path: '/v1/chat/completions',
  weight: 1, priority: 0, status: 1, remark: '',
  models: [] as ModelRow[],
})

function openEpCreate(v: Vendor) {
  epIsEdit.value = false
  epVendorId.value = v.id
  upstreamModels.value = []
  curlText.value = ''
  Object.assign(epForm, {
    id: 0, name: '', base_url: '', path: '/v1/chat/completions',
    weight: 1, priority: 0, status: 1, remark: '', models: [],
  })
  epVisible.value = true
}
function openEpEdit(ep: any, v: Vendor) {
  epIsEdit.value = true
  epVendorId.value = v.id
  upstreamModels.value = []
  curlText.value = ''
  Object.assign(epForm, {
    id: ep.id, name: ep.name, base_url: ep.base_url, path: ep.path,
    weight: ep.weight, priority: ep.priority, status: ep.status, remark: '',
    models: (ep.models || []).map((m: any) => ({
      model_name: m.model_name,
      upstream_model_name: m.upstream_model_name || '',
    })),
  })
  epVisible.value = true
}
function addModelRow() {
  epForm.models.push({ model_name: '', upstream_model_name: '' })
}
function removeModelRow(i: number) {
  epForm.models.splice(i, 1)
}

const upstreamModels = ref<string[]>([])
const fetchingModels = ref(false)
const selectOptions = computed(() =>
  upstreamModels.value.length > 0 ? upstreamModels.value : allModels.value,
)
function deprecatedUpstream(m: ModelRow): boolean {
  if (upstreamModels.value.length === 0 || !m.model_name) return false
  const up = (m.upstream_model_name || '').trim()
  if (up && upstreamModels.value.includes(up)) return false
  return !upstreamModels.value.includes(m.model_name.trim())
}
const deprecatedCount = computed(() => epForm.models.filter(deprecatedUpstream).length)
function removeDeprecated() {
  epForm.models = epForm.models.filter((m) => !deprecatedUpstream(m))
}
async function fetchUpstream() {
  fetchingModels.value = true
  try {
    let r: { models?: string[] } | undefined
    if (epIsEdit.value) {
      r = await apiFetchUpstreamModels(epForm.id)
    } else {
      if (!epForm.base_url) {
        ElMessage.warning('请先填写 Base URL（密钥取自厂商 Key 池）')
        return
      }
      const key = firstVendorKeyPlain()
      if (!key) {
        ElMessage.warning('厂商 Key 池为空：请先在上方 Key 池添加密钥，再从上游获取模型')
        return
      }
      r = await apiFetchUpstreamModelsByForm({
        base_url: epForm.base_url, path: epForm.path, upstream_key: key,
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
// 新建端点拉上游用厂商池第一把可用 Key 的明文（仅本地使用，不入库不回显）
async function firstVendorKeyPlain(): Promise<string> {
  const rows = await apiListVendorKeys(epVendorId.value)
  const usable = rows.find((k) => k.status === 1)
  if (!usable) return ''
  const full = await apiVendorKeyFull(epVendorId.value, usable.id)
  return full.key
}
function hasModelRow(name: string) {
  return epForm.models.some((m) => m.model_name === name)
}
function addUpstreamModel(name: string) {
  if (hasModelRow(name)) {
    ElMessage.info('已在列表中')
    return
  }
  epForm.models.push({ model_name: name, upstream_model_name: '' })
}

const curlText = ref('')
function applyCurl(): void {
  const parsed = parseCurl(curlText.value)
  if (!parsed) {
    ElMessage.warning('未识别出 URL：请粘贴厂商控制台复制的完整 curl 示例，或只贴接口地址')
    return
  }
  epForm.base_url = parsed.baseUrl
  if (parsed.path) epForm.path = parsed.path
  if (!epForm.name) epForm.name = parsed.host
  if (parsed.model && !hasModelRow(parsed.model)) {
    epForm.models.push({ model_name: parsed.model, upstream_model_name: '' })
  }
  if (parsed.apiKey) {
    ElMessage.info('curl 里的密钥请粘贴到「Key 池」新增框（Key 挂厂商，不随端点保存）')
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

async function submitEp() {
  if (!epForm.name || !epForm.base_url || epForm.models.length === 0) {
    ElMessage.warning('请填写端点名、Base URL 与至少一个模型')
    return
  }
  const payload = {
    name: epForm.name, vendor_id: epVendorId.value, base_url: epForm.base_url, path: epForm.path,
    weight: epForm.weight, priority: epForm.priority, status: epForm.status, remark: epForm.remark,
    models: epForm.models
      .filter((m) => m.model_name)
      .map((m) => ({
        model_name: m.model_name,
        upstream_model_name: m.upstream_model_name || null,
      })),
  }
  if (epIsEdit.value) {
    await apiUpdateChannel(epForm.id, payload)
    ElMessage.success('端点已更新')
  } else {
    await apiCreateChannel(payload)
    ElMessage.success('端点已创建')
  }
  epVisible.value = false
  load()
}

async function toggleEp(ep: any) {
  await apiUpdateChannelStatus(ep.id, ep.status === 1 ? 0 : 1)
  load()
}
async function removeEp(ep: any, v: Vendor) {
  await ElMessageBox.confirm(
    `删除端点「${ep.name}」？厂商 Key 池为共享资源不受影响，该端点的模型映射将一并移除。`, '提示',
    { type: 'warning' },
  )
  await apiDeleteChannel(ep.id)
  ElMessage.success('已删除')
  void v
  load()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>厂商管理（一行一厂商：Key 池共用轮询，端点=接入地址，模型按厂商分组）</span>
        <el-button type="primary" @click="openVendorCreate">新建厂商</el-button>
      </div>
    </template>

    <el-table :data="list" v-loading="loading" empty-text="还没有厂商。新建厂商 → 添加 Key → 添加端点（从上游获取模型）即可开始转发。">
      <el-table-column type="expand">
        <template #default="{ row: v }">
          <div class="expand-body">
            <!-- Key 池 -->
            <div class="section-title">
              Key 池（厂商下全部端点共用轮询）
              <el-button size="small" @click="openKeys(v)">
                {{ keysVendor === v.id ? '收起' : '管理 Key 池' }}
              </el-button>
            </div>
            <div v-if="keysVendor === v.id" v-loading="keysLoading" class="keys-panel">
              <el-alert v-if="quotaCoolingCount > 0" type="warning" :closable="false" show-icon
                :title="`${quotaCoolingCount} 把 Key 因厂商侧配额耗尽冷却中：冷却期自动跳过、到期自动恢复；限额恢复后可点「清除冷却」立即复用`" />
              <el-alert v-if="disabledKeyCount > 0" type="error" :closable="false" show-icon
                :title="`${disabledKeyCount} 把 Key 已禁用（多为上游 401/402/403 自动禁用：过期或余额不足）——更换后点「启用」恢复`" />
              <div class="keys-toolbar">
                <el-button size="small" type="primary" plain @click="toggleAdd">新增 Key</el-button>
                <span class="dim">同厂商全部端点共用这些 Key 轮询；429 冷却按厂商连坐</span>
              </div>
              <div v-if="addVisible" class="add-panel">
                <el-radio-group v-model="addMode" size="small">
                  <el-radio-button value="single">单个</el-radio-button>
                  <el-radio-button value="batch">批量</el-radio-button>
                </el-radio-group>
                <el-input v-if="addMode === 'single'" v-model="addKeyText" type="password"
                  placeholder="粘贴一把 Key" style="width: 320px" />
                <el-input v-else v-model="addKeyText" type="textarea" :rows="4"
                  placeholder="每行一把 Key（可混逗号分隔）；行尾可加 :权重，如 sk-xxx:3" style="width: 420px" />
                <span class="dim">统一权重</span>
                <el-input-number v-model="addWeight" :min="1" size="small" />
                <el-button size="small" type="primary" :loading="adding" @click="submitAddKeys">保存</el-button>
              </div>
              <el-table :data="keyList" size="small" :row-class-name="keyRowClass" max-height="320">
                <el-table-column label="Key" min-width="170">
                  <template #default="{ row }"><MaskedKey :masked="row.key_masked" :fetch-full="() => apiVendorKeyFull(keysVendor, row.id)" /></template>
                </el-table-column>
                <el-table-column prop="weight" label="权重" width="70" align="center" />
                <el-table-column label="状态" width="90" align="center">
                  <template #default="{ row }">
                    <el-tag v-if="row.status === 1 && row.quota_cooling" size="small" type="warning">配额冷却</el-tag>
                    <el-tag v-else-if="row.status === 1 && row.cooling" size="small" type="info">限流冷却</el-tag>
                    <el-tag v-else-if="row.status === 1" size="small" type="success">启用</el-tag>
                    <el-tag v-else size="small" type="danger">禁用</el-tag>
                  </template>
                </el-table-column>
                <el-table-column prop="remark" label="备注" min-width="150" show-overflow-tooltip />
                <el-table-column label="冷却" width="90" align="center">
                  <template #default="{ row }">
                    <el-button v-if="row.cooling && row.status === 1" size="small" link type="primary"
                      :loading="cooldownClearing === row.id" @click="clearCooldown(row)">清除</el-button>
                    <span v-else class="dim">—</span>
                  </template>
                </el-table-column>
                <el-table-column label="操作" width="150" align="center">
                  <template #default="{ row }">
                    <el-button size="small" link :type="row.status === 1 ? 'danger' : 'success'"
                      :loading="keyToggling === row.id" @click="toggleKey(row)">
                      {{ row.status === 1 ? '禁用' : '启用' }}
                    </el-button>
                    <el-button size="small" link type="danger" :loading="keyDeleting === row.id" @click="removeKey(row)">删除</el-button>
                  </template>
                </el-table-column>
              </el-table>
            </div>

            <!-- 端点 -->
            <div class="section-title">
              端点（接入地址：base_url + 路径 + 模型映射）
              <el-button size="small" type="primary" plain @click="openEpCreate(v)">添加端点</el-button>
            </div>
            <el-table :data="v.endpoints || []" size="small">
              <el-table-column prop="name" label="端点" min-width="120" />
              <el-table-column prop="base_url" label="Base URL" min-width="200" show-overflow-tooltip />
              <el-table-column prop="path" label="路径" min-width="140" />
              <el-table-column label="优先级/权重" width="100" align="center">
                <template #default="{ row: ep }">{{ ep.priority }} / {{ ep.weight }}</template>
              </el-table-column>
              <el-table-column label="模型" min-width="180">
                <template #default="{ row: ep }">
                  <code v-for="m in ep.models" :key="m.model_name" class="model-chip" :title="m.model_name">
                    {{ m.model_name }}
                  </code>
                </template>
              </el-table-column>
              <el-table-column label="状态" width="80" align="center">
                <template #default="{ row: ep }">
                  <el-tag :type="ep.status === 1 ? 'success' : 'info'" size="small" effect="plain">
                    {{ ep.status === 1 ? '启用' : '停用' }}
                  </el-tag>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="170" align="center">
                <template #default="{ row: ep }">
                  <el-button size="small" link type="primary" @click="openEpEdit(ep, v)">编辑</el-button>
                  <el-button size="small" link :type="ep.status === 1 ? 'danger' : 'success'" @click="toggleEp(ep)">
                    {{ ep.status === 1 ? '停用' : '启用' }}
                  </el-button>
                  <el-button size="small" link type="danger" @click="removeEp(ep, v)">删除</el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="厂商" min-width="160">
        <template #default="{ row: v }">
          <b>{{ v.display_name || v.name }}</b>
          <div class="dim">{{ v.name }}</div>
        </template>
      </el-table-column>
      <el-table-column label="Key 池" width="140" align="center">
        <template #default="{ row: v }">
          <el-tooltip v-if="v.has_key && deadKeys(v) > 0" placement="top"
            :content="`有 ${deadKeys(v)} 把 Key 被自动禁用（上游 401/402/403）——展开行到 Key 池看是哪一把，更换后点「启用」恢复`">
            <span class="key-warn">● {{ v.key_count }} 把（启 {{ v.key_active_count }}<template v-if="v.key_cooling_count">，冷 {{ v.key_cooling_count }}</template>）</span>
          </el-tooltip>
          <el-tooltip v-else-if="v.has_key && v.key_cooling_count" placement="top"
            :content="`${v.key_cooling_count} 把 Key 厂商侧配额冷却中：冷却期自动跳过、到期自动恢复`">
            <span class="key-warn">● {{ v.key_count }} 把（启 {{ v.key_active_count }}，冷 {{ v.key_cooling_count }}）</span>
          </el-tooltip>
          <span v-else-if="v.has_key" class="key-ok">● {{ v.key_count }} 把</span>
          <span v-else class="key-miss">● 缺失</span>
        </template>
      </el-table-column>
      <el-table-column label="模型/端点" width="110" align="center">
        <template #default="{ row: v }">{{ v.model_count }} / {{ v.endpoint_count }}</template>
      </el-table-column>
      <el-table-column label="客户开通" width="90" align="center">
        <template #default="{ row: v }">{{ v.org_count || 0 }} 家</template>
      </el-table-column>
      <el-table-column label="状态" width="80" align="center">
        <template #default="{ row: v }">
          <el-tag :type="v.status === 1 ? 'success' : 'info'" size="small" effect="plain">
            {{ v.status === 1 ? '启用' : '停用' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近测试" width="150">
        <template #default="{ row: v }">
          <template v-if="v.last_test_at">
            <span class="dim">{{ fmtTime(v.last_test_at) }}</span>
            <span :class="v.last_test_ok ? 'key-ok' : 'key-miss'" style="margin-left: 6px">
              {{ v.last_test_ok ? 'OK' : '失败' }}
            </span>
          </template>
          <span v-else class="dim">未测试</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="280" fixed="right">
        <template #default="{ row: v }">
          <el-button size="small" type="primary" :loading="testing === v.id" @click="testVendor(v)">测试</el-button>
          <el-button size="small" @click="openPrices(v)">定价</el-button>
          <el-button size="small" @click="openVendorEdit(v)">编辑</el-button>
          <el-button size="small" :type="v.status === 1 ? 'warning' : 'success'" plain @click="toggleVendor(v)">
            {{ v.status === 1 ? '停用' : '启用' }}
          </el-button>
          <el-button size="small" type="danger" plain @click="removeVendor(v)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 厂商价管理 -->
    <el-dialog v-model="priceVisible" :title="`厂商价：${priceVendor?.display_name || priceVendor?.name || ''}（组内全部模型统一生效，模型级覆盖优先）`" width="900px" top="6vh">
      <div class="section-title">
        生效优先级：模型覆盖价 &gt; 厂商价 &gt; 模型默认价
        <el-radio-group v-model="priceUnit" size="small" @change="onPriceUnitChange" style="margin-left: auto">
          <el-radio-button value="1k">元 / 1K token</el-radio-button>
          <el-radio-button value="1m">元 / 1M token</el-radio-button>
        </el-radio-group>
      </div>
      <div v-if="priceCandidates.length > 0" class="keys-toolbar">
        <el-select v-model="priceAddOrg" placeholder="选择客户" size="small" style="width: 200px" filterable>
          <el-option v-for="o in priceCandidates" :key="o.id" :label="o.name" :value="o.id" />
        </el-select>
        <el-button size="small" @click="addPriceRow">添加客户厂商价</el-button>
        <span class="dim">新客户默认 0 元（免费），保存前不影响计费</span>
      </div>
      <el-table :data="priceRows" v-loading="priceLoading" size="small" max-height="420"
        empty-text="还没有客户设厂商价（未设价的客户按模型级价格计费）">
        <el-table-column label="客户" min-width="130">
          <template #default="{ row: r }">
            <span :class="r.override ? '' : 'dim'">{{ r.org_name }}</span>
            <el-tag v-if="!r.override" size="small" type="info" effect="plain" style="margin-left: 6px">未设价</el-tag>
          </template>
        </el-table-column>
        <el-table-column width="190" align="center">
          <template #header>输入单价<span class="dim">（元/{{ priceUnitLabel }}）</span></template>
          <template #default="{ row: r }">
            <el-input-number v-if="drafts[r.org_id]" v-model="drafts[r.org_id].in" size="small"
              :min="0" :step="priceUnitStep" :precision="priceUnitPrecision" :controls="false" style="width: 140px" />
          </template>
        </el-table-column>
        <el-table-column width="190" align="center">
          <template #header>输出单价<span class="dim">（元/{{ priceUnitLabel }}）</span></template>
          <template #default="{ row: r }">
            <el-input-number v-if="drafts[r.org_id]" v-model="drafts[r.org_id].out" size="small"
              :min="0" :step="priceUnitStep" :precision="priceUnitPrecision" :controls="false" style="width: 140px" />
          </template>
        </el-table-column>
        <el-table-column width="210" align="center">
          <template #header>缓存命中单价<span class="dim">（0=同输入）</span></template>
          <template #default="{ row: r }">
            <el-input-number v-if="drafts[r.org_id]" v-model="drafts[r.org_id].cache" size="small"
              :min="0" :step="priceUnitStep" :precision="priceUnitPrecision" :controls="false" style="width: 140px" />
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" align="center">
          <template #default="{ row: r }">
            <el-button size="small" type="primary" :disabled="!priceDirty(r.org_id)"
              :loading="priceSaving === r.org_id" @click="savePriceRow(r.org_id)">保存</el-button>
            <el-button size="small" :disabled="!r.override" @click="resetPriceRow(r)">恢复</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>

    <!-- 厂商编辑 -->
    <el-dialog v-model="vEditVisible" :title="vIsEdit ? `编辑厂商：${vForm.name}` : '新建厂商'" width="520px">
      <el-form label-width="90px">
        <el-form-item label="标识名">
          <el-input v-model="vForm.name" placeholder="如 volces、volces/Thu（客户可见分组标签）" :disabled="vIsEdit && false" />
        </el-form-item>
        <el-form-item label="显示名">
          <el-input v-model="vForm.display_name" placeholder="空=用标识名" />
        </el-form-item>
        <el-form-item label="备注"><el-input v-model="vForm.remark" /></el-form-item>
        <el-form-item v-if="vIsEdit" label="状态">
          <el-switch v-model="vForm.status" :active-value="1" :inactive-value="0"
            active-text="启用" inactive-text="停用" />
          <span class="dim" style="margin-left: 8px">停用=该厂商全部端点不可路由（主开关）</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="vEditVisible = false">取消</el-button>
        <el-button type="primary" @click="submitVendor">保存</el-button>
      </template>
    </el-dialog>

    <!-- 端点编辑 -->
    <el-dialog v-model="epVisible" :title="epIsEdit ? `编辑端点：${epForm.name}` : `添加端点（厂商 #${epVendorId}）`" width="760px" top="6vh">
      <el-form label-width="90px">
        <div class="section-title" style="margin-bottom: 8px">接入信息（密钥在厂商 Key 池，此处不填）</div>
        <el-form-item label="端点名"><el-input v-model="epForm.name" placeholder="如 火山方舟-对话" /></el-form-item>
        <el-form-item label="Base URL"><el-input v-model="epForm.base_url" placeholder="https://ark.cn-beijing.volces.com/api/v3" /></el-form-item>
        <el-form-item label="路径">
          <el-input v-model="epForm.path" placeholder="/chat/completions（视频端点 /contents/generations/tasks）" style="width: 360px" />
          <el-tooltip placement="top" content="优先级大者优先，同优先级按端点权重分流；降级备用按优先级顺延">
            <span class="dim" style="margin-left: 12px">优先级</span>
          </el-tooltip>
          <el-input-number v-model="epForm.priority" :min="0" size="small" />
          <span class="dim" style="margin-left: 12px">权重</span>
          <el-input-number v-model="epForm.weight" :min="1" size="small" />
        </el-form-item>

        <div class="section-title" style="margin: 8px 0">
          模型映射（对外名 → 上游名，留空=同名透传）
          <el-button size="small" @click="fetchUpstream" :loading="fetchingModels">从上游获取</el-button>
          <el-button v-if="deprecatedCount > 0" size="small" type="warning" plain @click="removeDeprecated">
            清理上游已下架（{{ deprecatedCount }}）
          </el-button>
        </div>
        <div v-if="!epIsEdit" class="curl-row">
          <el-input v-model="curlText" placeholder="粘贴厂商 curl 示例快速接入（解析出 Base URL/路径/模型名；密钥请粘到 Key 池）"
            @paste="onCurlPaste" />
          <el-button @click="applyCurl">解析</el-button>
        </div>
        <el-form-item v-for="(m, i) in epForm.models" :key="i" :label="`模型 ${i + 1}`">
          <el-select v-model="m.model_name" filterable allow-create default-first-option
            placeholder="对外模型名（可手输）" style="width: 260px">
            <el-option v-for="name in selectOptions" :key="name" :label="name" :value="name" />
          </el-select>
          <el-input v-model="m.upstream_model_name" placeholder="上游名（空=同名）" style="width: 240px; margin-left: 8px" />
          <el-button link type="danger" style="margin-left: 8px" @click="removeModelRow(i)">移除</el-button>
        </el-form-item>
        <el-form-item>
          <el-button size="small" @click="addModelRow">+ 添加模型</el-button>
          <span class="dim" style="margin-left: 8px">模型保存后自动登记到「模型定价」（价格 0，需定价）并归属本厂商</span>
        </el-form-item>
        <el-form-item label="状态">
          <el-switch v-model="epForm.status" :active-value="1" :inactive-value="0" active-text="启用" inactive-text="停用" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="epVisible = false">取消</el-button>
        <el-button type="primary" @click="submitEp">保存</el-button>
      </template>
    </el-dialog>
  </el-card>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.dim { color: var(--tg-muted); font-size: 12px; }
.section-title {
  display: flex; align-items: center; gap: 10px;
  font-size: 13px; font-weight: 600; color: var(--tg-ink);
  margin: 10px 0 6px;
}
.expand-body { padding: 4px 12px 12px; }
.keys-panel { display: flex; flex-direction: column; gap: 8px; margin-bottom: 12px; }
.keys-toolbar { display: flex; align-items: center; gap: 10px; }
.add-panel { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.curl-row { display: flex; gap: 8px; margin-bottom: 8px; }
.model-chip {
  display: inline-block; margin: 1px 4px 1px 0; padding: 0 6px;
  background: var(--tg-green-wash); border-radius: 4px; font-size: 12px;
}
.key-ok { color: var(--tg-green-ink); }
.key-warn { color: #b45309; }
.key-miss { color: var(--tg-red); }
:deep(.key-dead-row) { background: #fef2f2; }
:deep(.quota-cooling-row) { background: #fffbeb; }
:deep(.cooling-row) { background: #f5f5f4; }
</style>
