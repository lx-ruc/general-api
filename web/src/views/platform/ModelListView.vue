<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  apiListModels, apiUpdateModel, apiDeleteModel,
  apiListOrgModelPrices, apiSetOrgModelPrice, apiDeleteOrgModelPrice, apiListOrgs, apiUpdateOrg,
  type MModel, type OrgModelPriceRow,
} from '../../api/platform'
import { yuan1KToPoints, pointsToYuan1K, yuan1MToPoints, pointsToYuan1M } from '../../utils/format'
import { usePpy } from '../../composables/usePpy'

const ppy = usePpy()

// ---- 定价录入单位（元/1K ↔ 元/1M，存储口径不变仍是点/百万token，仅录入/回显换算）----
const unit = ref<'1k' | '1m'>('1k')
const unitLabel = computed(() => (unit.value === '1k' ? '1K' : '1M'))
const unitStep = computed(() => (unit.value === '1k' ? 0.001 : 0.1))
const unitPrecision = computed(() => (unit.value === '1k' ? 6 : 4))

// 点数 → 当前单位显示值（回显）
function toDisplay(points: number): number {
  return unit.value === '1k'
    ? pointsToYuan1K(points, ppy.value)
    : pointsToYuan1M(points, ppy.value)
}
// 当前单位显示值 → 点数（提交）
function fromDisplay(v: number): number {
  return unit.value === '1k'
    ? yuan1KToPoints(v, ppy.value)
    : yuan1MToPoints(v, ppy.value)
}

// 切换单位：表单三价与客户价草稿/快照同批 ×1000 或 ÷1000（快照同口径换算，避免浮点误差误判 dirty）
function onUnitChange() {
  const f = unit.value === '1k' ? (v: number) => v / 1000 : (v: number) => v * 1000
  const conv = (v: number) => Number((f(v || 0)).toFixed(10))
  form.input_price = conv(form.input_price)
  form.output_price = conv(form.output_price)
  form.input_cache_hit_price = conv(form.input_cache_hit_price)
  for (const k of Object.keys(drafts)) {
    const id = Number(k)
    drafts[id] = {
      ...drafts[id], in: conv(drafts[id].in), out: conv(drafts[id].out), cache: conv(drafts[id].cache),
    }
    if (snapshots[id]) {
      snapshots[id] = {
        ...snapshots[id], in: conv(snapshots[id].in), out: conv(snapshots[id].out), cache: conv(snapshots[id].cache),
      }
    }
  }
}

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
    : '还没有模型。到「渠道管理」新建渠道（填密钥 → 从上游获取模型），模型会自动登记到这里，价格在本页定价。',
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

// ---- 定价弹窗：默认价 + 客户差异化定价 ----
// 模型不在此手动新建：只能经「渠道管理」配密钥从上游获取后自动登记（此处只改价/状态/备注）
const pricingVisible = ref(false)
const form = reactive({
  id: 0, name: '', display_name: '', vendor: '',
  input_price: 0, output_price: 0, input_cache_hit_price: 0,
  cost_input_price: 0, cost_output_price: 0, status: 1, remark: '',
})

async function openPricing(m: MModel) {
  Object.assign(form, {
    id: m.id, name: m.name, display_name: m.display_name, vendor: m.vendor,
    // 表单按当前单位（元/1K 或 元/1M）编辑，入库前换算回点（存储口径不变：点/百万token）
    input_price: toDisplay(m.input_price), output_price: toDisplay(m.output_price),
    input_cache_hit_price: toDisplay(m.input_cache_hit_price),
    cost_input_price: m.cost_input_price, cost_output_price: m.cost_output_price,
    status: m.status, remark: m.remark,
  })
  pricingVisible.value = true
  await loadOrgPrices(m.name)
}

async function submit() {
  await apiUpdateModel(form.id, {
    display_name: form.display_name, vendor: form.vendor,
    input_price: fromDisplay(form.input_price), output_price: fromDisplay(form.output_price),
    input_cache_hit_price: fromDisplay(form.input_cache_hit_price),
    cost_input_price: form.cost_input_price, cost_output_price: form.cost_output_price,
    status: form.status, remark: form.remark,
  })
  ElMessage.success('默认价已更新')
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

// ---- 客户差异化定价（弹窗下半区）----
const orgPrices = ref<OrgModelPriceRow[]>([])
const orgPricesLoading = ref(false)
const allOrgs = ref<{ id: number; name: string; discount_permille?: number }[]>([])
const addOrgId = ref<number | undefined>(undefined)

async function loadOrgsOnce() {
  if (allOrgs.value.length > 0) return
  const resp = await apiListOrgs({ page: 1, page_size: 1000 })
  allOrgs.value = (resp.list ?? []).map((o: any) =>
    ({ id: o.id, name: o.name, discount_permille: o.discount_permille ?? 1000 }))
}

async function loadOrgPrices(modelName: string) {
  orgPricesLoading.value = true
  try {
    await loadOrgsOnce()
    orgPrices.value = await apiListOrgModelPrices(modelName)
    seedDrafts()
  } finally {
    orgPricesLoading.value = false
  }
}

// 弹窗里还没出现的客户（支持先定价后授权）
const candidateOrgs = computed(() => {
  const listed = new Set(orgPrices.value.map((r) => r.org_id))
  return allOrgs.value.filter((o) => !listed.has(o.id))
})

// 客户价与折扣均改为手动保存：编辑先落在 drafts（单价=当前单位，折扣=折数），
// 点「保存客户价」才逐客户提交（价格 upsert + 折扣 apiUpdateOrg）。
// snapshots 是打开弹窗时的原始值（与 drafts 同单位同口径换算避免浮点误差误判 dirty）
type OrgDraft = { in: number; out: number; cache: number; zhe: number }
const drafts = reactive<Record<number, OrgDraft>>({})
const snapshots = reactive<Record<number, OrgDraft>>({})

function seedDrafts() {
  for (const k of Object.keys(drafts)) delete drafts[k]
  for (const k of Object.keys(snapshots)) delete snapshots[k]
  for (const r of orgPrices.value) {
    const d = {
      in: toDisplay(r.input_price), out: toDisplay(r.output_price),
      cache: toDisplay(r.input_cache_hit_price),
      zhe: (r.discount_permille ?? 1000) / 100,
    }
    drafts[r.org_id] = { ...d }
    snapshots[r.org_id] = { ...d }
  }
}

function isDirty(orgId: number): boolean {
  const d = drafts[orgId]
  const s = snapshots[orgId]
  return !!d && !!s && (d.in !== s.in || d.out !== s.out || d.cache !== s.cache || d.zhe !== s.zhe)
}

const dirtyCount = computed(() => orgPrices.value.filter((r) => isDirty(r.org_id)).length)
const savingOrgs = ref(false)

// 逐客户提交有改动的行：价格变化则三价一起 upsert，折扣变化则 apiUpdateOrg
// （org 级全局折扣，非仅本模型）；input-number 清空是 null，单价按 0、折扣按 10（不打折）处理。
// 折扣越界先整体拒绝（不产生半保存状态）
async function saveOrgPrices() {
  if (dirtyCount.value === 0) return
  for (const r of orgPrices.value) {
    if (!isDirty(r.org_id)) continue
    const zhe = drafts[r.org_id].zhe ?? 10
    if (zhe < 0.1 || zhe > 10) {
      ElMessage.warning(`「${r.org_name}」折扣取值 0.1 ~ 10 折`)
      return
    }
  }
  savingOrgs.value = true
  try {
    let n = 0
    for (const r of orgPrices.value) {
      if (!isDirty(r.org_id)) continue
      const d = drafts[r.org_id]
      const s = snapshots[r.org_id]
      if (d.in !== s.in || d.out !== s.out || d.cache !== s.cache) {
        await apiSetOrgModelPrice(form.name, {
          org_id: r.org_id,
          input_price: fromDisplay(d.in || 0),
          output_price: fromDisplay(d.out || 0),
          input_cache_hit_price: fromDisplay(d.cache || 0),
        })
      }
      const zhe = d.zhe ?? 10
      if (zhe !== s.zhe) {
        await apiUpdateOrg(r.org_id, { discount_permille: Math.round(zhe * 100) })
      }
      n++
    }
    ElMessage.success(`${form.name} 的 ${n} 家客户价/折扣已保存`)
    orgPrices.value = await apiListOrgModelPrices(form.name)
    seedDrafts()
  } finally {
    savingOrgs.value = false
  }
}

function addOrgPrice() {
  const orgId = addOrgId.value
  if (!orgId) {
    ElMessage.warning('请先选择客户')
    return
  }
  // 新增行以当前模型默认价起步（从最新列表取，避免弹窗内改过默认价后种子价过期），保存时才落库
  const org = allOrgs.value.find((o) => o.id === orgId)
  const m = list.value.find((x) => x.name === form.name)
  orgPrices.value = [
    ...orgPrices.value,
    {
      org_id: orgId, org_name: org?.name ?? `#${orgId}`, member_count: 0, override: false,
      discount_permille: org?.discount_permille ?? 1000,
      input_price: m.input_price, output_price: m.output_price,
      input_cache_hit_price: m.input_cache_hit_price, remark: '',
    },
  ]
  const d = {
    in: toDisplay(m.input_price), out: toDisplay(m.output_price),
    cache: toDisplay(m.input_cache_hit_price),
    zhe: (org?.discount_permille ?? 1000) / 100,
  }
  drafts[orgId] = { ...d }
  snapshots[orgId] = { ...d }
  addOrgId.value = undefined
}

async function resetOrgPrice(row: OrgModelPriceRow) {
  await ElMessageBox.confirm(
    `恢复 ${row.org_name} 使用模型默认价？`, '恢复默认',
    { type: 'warning' },
  )
  await apiDeleteOrgModelPrice(form.name, row.org_id)
  ElMessage.success('已恢复默认价')
  orgPrices.value = await apiListOrgModelPrices(form.name)
  seedDrafts()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>模型定价（点「定价」设置模型默认价与各客户差异化价格；录入单位可选「元 / 1K token」或「元 / 1M token」，在定价弹窗顶部切换）</span>
        <div class="header-actions">
          <el-checkbox v-model="onlyLive">只看已接通（{{ liveCount }}/{{ list.length }}）</el-checkbox>
        </div>
      </div>
    </template>

    <el-table :data="filteredList" v-loading="loading" :empty-text="emptyText">
      <el-table-column prop="name" label="模型名" min-width="150">
        <template #default="{ row }"><code>{{ row.name }}</code></template>
      </el-table-column>
      <el-table-column prop="display_name" label="显示名" min-width="140" />
      <el-table-column prop="vendor" label="厂商" width="90" />
      <el-table-column label="可用渠道" width="110" align="center"
        title="已接通（启用且有可用密钥）的渠道数；点击查看具体是哪些渠道">
        <template #default="{ row }">
          <el-popover v-if="(row.channel_count ?? 0) > 0" trigger="click" width="280" placement="right">
            <template #reference>
              <el-tag type="success" effect="plain" size="small" class="chan-link">
                {{ row.channel_count }} 渠道
              </el-tag>
            </template>
            <div class="chan-pop-title">接通「{{ row.name }}」的渠道</div>
            <div v-for="n in row.channel_names ?? []" :key="n" class="chan-item">{{ n }}</div>
          </el-popover>
          <el-tag v-else type="info" effect="plain" size="small">未接</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="调用状态" width="200" class-name="status-cell"
        title="「上游密钥不可用」= 已接启用渠道但密钥全部失效/未配置（如被上游 401 自动禁用），到「渠道管理」换密钥即恢复。未定价不拦截调用、按 0 元计费：设了客户价的客户按客户价计费，其余客户免费。给模型设默认价或给全部客户设价后转「可用」">
        <template #default="{ row }">
          <el-tooltip v-if="row.status !== 1 && (row.auto_disabled_at ?? 0) > 0" placement="top"
            content="所有承接渠道的密钥均不可用（Key 失效/被上游吊销/渠道停用），已自动停用；到「渠道管理」更换密钥或恢复渠道后自动恢复启用，授权与定价不受影响">
            <el-tag type="danger" effect="plain" size="small">自动停用（密钥失效）</el-tag>
          </el-tooltip>
          <el-tag v-else-if="row.status !== 1" type="info" effect="plain" size="small">已停用</el-tag>
          <el-tag v-else-if="!row.routable && row.has_enabled_channel" type="danger" effect="plain" size="small">上游密钥不可用</el-tag>
          <el-tag v-else-if="!row.routable" type="warning" effect="plain" size="small">未接渠道</el-tag>
          <el-tag v-else-if="row.input_price === 0 && row.output_price === 0 && (row.org_price_count ?? 0) > 0"
            type="warning" effect="plain" size="small">仅客户价（{{ row.org_price_count }} 家）</el-tag>
          <el-tag v-else-if="row.input_price === 0 && row.output_price === 0" type="danger" effect="plain" size="small">不可用（未确定默认价格）</el-tag>
          <el-tag v-else type="success" effect="plain" size="small">可用</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="remark" label="备注" min-width="120" show-overflow-tooltip />
      <el-table-column label="操作" width="140" fixed="right">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="openPricing(row)">定价</el-button>
          <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>
  </el-card>

  <el-dialog v-model="pricingVisible" :title="`定价：${form.name}`" width="1120px" top="6vh">
    <el-form label-width="155px">
      <div class="section-title">
        模型默认价（未单独定价的客户按此计费）
        <el-radio-group v-model="unit" size="small" @change="onUnitChange" class="unit-switch">
          <el-radio-button value="1k">元 / 1K token</el-radio-button>
          <el-radio-button value="1m">元 / 1M token</el-radio-button>
        </el-radio-group>
      </div>
      <el-row :gutter="12">
        <el-col :span="12">
          <el-form-item :label="`输入单价（元/${unitLabel} token）`">
            <el-input-number v-model="form.input_price" :min="0" :step="unitStep" :precision="unitPrecision" />
          </el-form-item>
        </el-col>
        <el-col :span="12">
          <el-form-item :label="`输出单价（元/${unitLabel} token）`">
            <el-input-number v-model="form.output_price" :min="0" :step="unitStep" :precision="unitPrecision" />
          </el-form-item>
        </el-col>
      </el-row>
      <el-form-item :label="`缓存命中单价（元/${unitLabel} token）`">
        <el-input-number v-model="form.input_cache_hit_price" :min="0" :step="unitStep" :precision="unitPrecision" />
        <span class="tip">提示缓存命中的输入 tokens 按此价计；0 = 同输入单价</span>
      </el-form-item>
      <el-row :gutter="12">
        <el-col :span="12"><el-form-item label="显示名"><el-input v-model="form.display_name" /></el-form-item></el-col>
        <el-col :span="12"><el-form-item label="厂商"><el-input v-model="form.vendor" /></el-form-item></el-col>
      </el-row>
      <el-form-item label="状态">
        <el-switch v-model="form.status" :active-value="1" :inactive-value="0" active-text="启用" inactive-text="停用" />
        <el-input v-model="form.remark" placeholder="备注" style="margin-left: 24px; width: 320px" />
      </el-form-item>
    </el-form>
    <div class="dialog-actions">
      <el-button type="primary" @click="submit">保存默认价</el-button>
    </div>

    <el-divider />

    <div class="section-title">
      客户差异化定价
      <span class="dim">为单个客户设价后按客户价计费，未设价的走上方默认价；「折扣」为该客户的全局折扣，实际计费 = 本表单价 × 折扣（默认价同样打折）；单价与折扣修改完点右下角「保存客户价」一次提交</span>
    </div>
    <div class="org-toolbar" v-if="candidateOrgs.length > 0">
      <el-select v-model="addOrgId" placeholder="选择客户" size="small" style="width: 200px">
        <el-option v-for="o in candidateOrgs" :key="o.id" :label="o.name" :value="o.id" />
      </el-select>
      <el-button size="small" @click="addOrgPrice">添加客户定价</el-button>
    </div>
    <el-table :data="orgPrices" v-loading="orgPricesLoading" size="small"
      empty-text="还没有客户使用该模型（授权子账号后出现；也可先添加客户定价）">
      <el-table-column label="客户" min-width="150">
        <template #default="{ row: r }">
          <span :class="r.override ? 'custom-name' : ''">{{ r.org_name }}</span>
          <el-tag v-if="!r.override" size="small" type="info" effect="plain" class="def-tag">默认</el-tag>
          <el-tag v-if="isDirty(r.org_id)" size="small" type="warning" effect="plain" class="def-tag">未保存</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="已授权子账号" width="110" align="center">
        <template #default="{ row: r }">{{ r.member_count }} 人</template>
      </el-table-column>
      <el-table-column width="105" align="center">
        <template #header>
          <el-tooltip placement="top"
            content="客户折扣为该客户的全局设置（非仅本模型，全部模型联动）：实际计费 = 本表单价 × 折扣，走默认价同样打折；10 = 不打折。与单价一起点右下角「保存客户价」提交">
            <span>折扣（折）</span>
          </el-tooltip>
        </template>
        <template #default="{ row: r }">
          <el-input-number v-model="drafts[r.org_id].zhe"
            :min="0.1" :max="10" :precision="1" :step="0.5" size="small"
            style="width: 78px" :controls="false" />
        </template>
      </el-table-column>
      <el-table-column :label="`输入单价（元/${unitLabel} token）`" width="195" align="center">
        <template #default="{ row: r }">
          <el-input-number v-model="drafts[r.org_id].in" :min="0" :step="unitStep" :precision="unitPrecision"
            size="small" style="width: 130px" :controls="false" />
        </template>
      </el-table-column>
      <el-table-column :label="`输出单价（元/${unitLabel} token）`" width="195" align="center">
        <template #default="{ row: r }">
          <el-input-number v-model="drafts[r.org_id].out" :min="0" :step="unitStep" :precision="unitPrecision"
            size="small" style="width: 130px" :controls="false" />
        </template>
      </el-table-column>
      <el-table-column :label="`缓存命中（元/${unitLabel} token，0=同输入）`" width="220" align="center">
        <template #default="{ row: r }">
          <el-input-number v-model="drafts[r.org_id].cache" :min="0" :step="unitStep" :precision="unitPrecision"
            size="small" style="width: 130px" :controls="false" />
        </template>
      </el-table-column>
      <el-table-column label="操作" width="110" fixed="right">
        <template #default="{ row: r }">
          <el-button v-if="r.override" size="small" @click="resetOrgPrice(r)">恢复默认</el-button>
        </template>
      </el-table-column>
    </el-table>
    <div class="dialog-actions" style="margin-top: 12px">
      <el-button type="primary" :loading="savingOrgs" :disabled="dirtyCount === 0"
        @click="saveOrgPrices">保存客户价{{ dirtyCount > 0 ? `（${dirtyCount} 家）` : '' }}</el-button>
    </div>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-actions { display: flex; align-items: center; gap: 12px; }
.tip { margin-left: 8px; font-size: 12px; color: #909399; }
.dim { color: var(--tg-muted); font-size: 11px; }
.green { color: var(--tg-green-ink); }
/* 状态标签强制单行：列宽已足够，避免窄列下换行/截断挤出视觉残点 */
:deep(.status-cell .el-tag) { white-space: nowrap; }
/* 可用渠道数可点击：点开看具体渠道名 */
.chan-link { cursor: pointer; }
.chan-pop-title { font-weight: 600; font-size: 12px; margin-bottom: 6px; color: var(--el-text-color-regular); }
.chan-item { font-size: 13px; line-height: 1.9; }
/* 已设客户价的客户名用蓝紫区分走默认价的 */
.custom-name { color: #7c3aed; font-weight: 600; }
/* 弹窗分区 */
.section-title { font-weight: 600; margin-bottom: 12px; display: flex; align-items: baseline; gap: 8px; }
.unit-switch { margin-left: auto; }
.dialog-actions { text-align: right; margin-bottom: 4px; }
.org-toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.def-tag { margin-left: 6px; }
</style>
