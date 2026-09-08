<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import dayjs from 'dayjs'
import {
  apiOrgCostCenters, apiCreateCostCenter, apiUpdateCostCenter, apiUpdateCostCenterConfig,
  apiCostCenterReport, type CostCenter, type CostReportRow,
} from '../../api/org'
import { fmtQuota, pointsToYuan } from '../../utils/format'

const list = ref<CostCenter[]>([])
const requireCC = ref(0)
const tab = ref('centers')

async function load() {
  const data = await apiOrgCostCenters()
  list.value = data.list || []
  requireCC.value = data.require_cost_center
}
onMounted(load)

const createVisible = ref(false)
const newName = ref('')
const creating = ref(false)

async function submitCreate() {
  if (!newName.value.trim()) return
  creating.value = true
  try {
    await apiCreateCostCenter(newName.value.trim())
    ElMessage.success('已创建')
    createVisible.value = false
    newName.value = ''
    load()
  } finally {
    creating.value = false
  }
}

async function rename(cc: CostCenter) {
  try {
    const { value } = await ElMessageBox.prompt('新的中心名称', `重命名「${cc.name}」`, {
      inputValue: cc.name, inputPattern: /^.{1,64}$/, inputErrorMessage: '名称不能为空',
    })
    await apiUpdateCostCenter(cc.id, { name: value.trim() })
    ElMessage.success('已重命名')
    load()
  } catch { /* 取消 */ }
}

async function toggleArchive(cc: CostCenter) {
  const to = cc.status === 1 ? 0 : 1
  const tip = to === 0
    ? `归档「${cc.name}」？新建密钥下拉将不再出现，历史报表保留。`
    : `恢复「${cc.name}」为启用？`
  await ElMessageBox.confirm(tip, '提示', { type: 'warning' })
  await apiUpdateCostCenter(cc.id, { status: to })
  ElMessage.success(to === 0 ? '已归档' : '已恢复')
  load()
}

async function toggleRequire(v: number | string | boolean) {
  await apiUpdateCostCenterConfig(Number(v) ? 1 : 0)
  requireCC.value = Number(v) ? 1 : 0
  ElMessage.success(requireCC.value ? '已开启强制归集：新建密钥必须选择成本中心' : '已关闭强制归集')
}

// ---- 报表 ----
const filter = reactive({
  range: [dayjs().startOf('month').format('YYYY-MM-DD'), dayjs().format('YYYY-MM-DD')] as [string, string],
  center_id: 0,
  model: '',
})
const report = ref<CostReportRow[]>([])
const summary = reactive({ total_cost: 0, unallocated_cost: 0, unallocated_pct: 0 })

async function loadReport() {
  const [s, e] = filter.range || []
  const data = await apiCostCenterReport({
    start: s ? String(dayjs(s).startOf('day').unix()) : '',
    end: e ? String(dayjs(e).add(1, 'day').startOf('day').unix()) : '',
    center_id: filter.center_id || '',
    model: filter.model || '',
  })
  report.value = data.list || []
  summary.total_cost = data.total_cost
  summary.unallocated_cost = data.unallocated_cost
  summary.unallocated_pct = data.unallocated_pct
}

function centerName(r: CostReportRow): string {
  if (r.cost_center_id == null) return '未归集'
  return r.center_status === 0 ? `${r.center_name}（已归档）` : r.center_name
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>成本中心</span>
        <div class="header-right">
          <el-switch :model-value="requireCC === 1" active-text="强制归集" @change="toggleRequire" />
          <el-button type="primary" @click="createVisible = true">新建中心</el-button>
        </div>
      </div>
    </template>

    <el-alert v-if="requireCC === 0" type="info" :closable="false" show-icon style="margin-bottom: 12px"
      title="未开启强制归集：未归集密钥的消耗会在报表中单独披露（置底显示）" />
    <el-alert v-else type="warning" :closable="false" show-icon style="margin-bottom: 12px"
      title="已开启强制归集：子账号新建密钥必须选择成本中心" />

    <el-table :data="list">
      <el-table-column prop="name" label="名称" min-width="160">
        <template #default="{ row }">
          {{ row.name }}
          <el-tag v-if="row.status === 0" type="info" size="small" style="margin-left: 6px">已归档</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="本月消耗" width="180">
        <template #default="{ row }">{{ fmtQuota(row.month_cost) }}<span class="dim"> · ¥{{ pointsToYuan(row.month_cost) }}</span></template>
      </el-table-column>
      <el-table-column label="挂靠密钥" width="100">
        <template #default="{ row }">{{ row.key_count }}</template>
      </el-table-column>
      <el-table-column label="操作" width="160">
        <template #default="{ row }">
          <el-button size="small" @click="rename(row)">改名</el-button>
          <el-button size="small" :type="row.status === 1 ? 'warning' : 'success'" @click="toggleArchive(row)">
            {{ row.status === 1 ? '归档' : '恢复' }}
          </el-button>
        </template>
      </el-table-column>
      <template #empty>还没有成本中心。先建一个（如「AI客服」「数据分析」），再在密钥上归集。</template>
    </el-table>
    <div class="dim tip">归档不删除：历史报表与账单继续显示该中心数据；改名不影响历史。</div>
  </el-card>

  <el-card shadow="never" style="margin-top: 16px">
    <template #header><span>成本报表（中心 × 模型 × 日）</span></template>
    <el-form inline @submit.enter.prevent="loadReport">
      <el-form-item label="日期">
        <el-date-picker v-model="filter.range" type="daterange" value-format="YYYY-MM-DD"
          start-placeholder="开始" end-placeholder="结束" style="width: 240px" />
      </el-form-item>
      <el-form-item label="中心">
        <el-select v-model="filter.center_id" clearable placeholder="全部" style="width: 160px">
          <el-option v-for="cc in list.filter((c) => c.status === 1)" :key="cc.id" :label="cc.name" :value="cc.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="模型">
        <el-input v-model="filter.model" placeholder="如 deepseek-chat" clearable style="width: 180px" />
      </el-form-item>
      <el-form-item><el-button type="primary" @click="loadReport">查询</el-button></el-form-item>
    </el-form>

    <el-alert v-if="summary.unallocated_cost > 0" type="warning" :closable="false" show-icon style="margin-bottom: 12px"
      :title="`未归集消耗 ¥${pointsToYuan(summary.unallocated_cost)}（${summary.unallocated_pct.toFixed(1)}%）——建议在密钥一览中补派中心`" />

    <el-table :data="report">
      <el-table-column label="成本中心" min-width="150">
        <template #default="{ row }">
          <span :class="{ dim: row.cost_center_id == null }">{{ centerName(row) }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="model_name" label="模型" min-width="140" />
      <el-table-column prop="day" label="日期" width="110" />
      <el-table-column prop="requests" label="请求数" width="90" />
      <el-table-column prop="cache_hits" label="缓存命中" width="90" />
      <el-table-column prop="prompt_tokens" label="输入 tokens" width="120" />
      <el-table-column prop="completion_tokens" label="输出 tokens" width="120" />
      <el-table-column label="费用" width="140">
        <template #default="{ row }">¥{{ pointsToYuan(row.cost) }}</template>
      </el-table-column>
      <template #empty>暂无数据</template>
    </el-table>
    <div v-if="report.length" class="dim tip">
      合计 ¥{{ pointsToYuan(summary.total_cost) }}；未归集恒置底
    </div>
  </el-card>

  <el-dialog v-model="createVisible" title="新建成本中心" width="420px">
    <el-input v-model="newName" placeholder="如：AI客服 / 数据分析 / 内部工具" maxlength="64" show-word-limit />
    <template #footer>
      <el-button @click="createVisible = false">取消</el-button>
      <el-button type="primary" :loading="creating" :disabled="!newName.trim()" @click="submitCreate">创建</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.header-right { display: flex; align-items: center; gap: 16px; }
.dim { color: var(--el-text-color-secondary); }
.tip { font-size: 12px; margin-top: 8px; }
</style>
