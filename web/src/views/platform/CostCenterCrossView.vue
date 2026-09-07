<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { apiCostCenterCross, type CostCrossRow } from '../../api/platform'
import { pointsToYuan, fmtPoints } from '../../utils/format'

const filter = reactive({
  range: [dayjs().startOf('month').format('YYYY-MM-DD'), dayjs().format('YYYY-MM-DD')] as [string, string],
  org_id: 0,
})
const list = ref<CostCrossRow[]>([])
const summary = reactive({ total_cost: 0, total_vendor_cost: 0, total_margin: 0 })

async function load() {
  const [s, e] = filter.range || []
  const data = await apiCostCenterCross({
    start: s ? String(dayjs(s).startOf('day').unix()) : '',
    end: e ? String(dayjs(e).add(1, 'day').startOf('day').unix()) : '',
    org_id: filter.org_id || '',
  })
  list.value = data.list || []
  summary.total_cost = data.total_cost
  summary.total_vendor_cost = data.total_vendor_cost
  summary.total_margin = data.total_margin
}
onMounted(load)

function centerName(r: CostCrossRow): string {
  if (r.cost_center_id == null) return '未归集'
  return r.center_status === 0 ? `${r.center_name}（已归档）` : r.center_name
}

function spanOrg({ rowIndex, columnIndex }: { rowIndex: number; columnIndex: number }) {
  // org 名列跨行合并：同 org 连续行
  if (columnIndex !== 0) return
  const rows = list.value
  const r = rows[rowIndex]
  if (rowIndex > 0 && rows[rowIndex - 1].org_id === r.org_id) return { rowspan: 0, colspan: 1 }
  let n = 1
  while (rowIndex + n < rows.length && rows[rowIndex + n].org_id === r.org_id) n++
  return { rowspan: n, colspan: 1 }
}
</script>

<template>
  <el-card shadow="never">
    <template #header>成本中心交叉报表（各公司 × 中心，含毛利）</template>
    <el-form inline @submit.enter.prevent="load">
      <el-form-item label="日期">
        <el-date-picker v-model="filter.range" type="daterange" value-format="YYYY-MM-DD"
          start-placeholder="开始" end-placeholder="结束" style="width: 240px" />
      </el-form-item>
      <el-form-item label="公司 ID">
        <el-input-number v-model="filter.org_id" :min="0" :controls="false" placeholder="全部" style="width: 120px" />
      </el-form-item>
      <el-form-item><el-button type="primary" @click="load">查询</el-button></el-form-item>
    </el-form>

    <el-table :data="list" :span-method="spanOrg" border>
      <el-table-column prop="org_name" label="公司" min-width="130" />
      <el-table-column label="成本中心" min-width="130">
        <template #default="{ row }">
          <span :class="{ dim: row.cost_center_id == null }">{{ centerName(row) }}</span>
        </template>
      </el-table-column>
      <el-table-column prop="requests" label="请求数" width="90" />
      <el-table-column prop="cache_hits" label="缓存命中" width="90" />
      <el-table-column label="输入 tokens" width="110">
        <template #default="{ row }">{{ fmtPoints(row.prompt_tokens) }}</template>
      </el-table-column>
      <el-table-column label="输出 tokens" width="110">
        <template #default="{ row }">{{ fmtPoints(row.completion_tokens) }}</template>
      </el-table-column>
      <el-table-column label="营收" width="110">
        <template #default="{ row }">¥{{ pointsToYuan(row.cost) }}</template>
      </el-table-column>
      <el-table-column label="厂商成本" width="110">
        <template #default="{ row }">¥{{ pointsToYuan(row.vendor_cost) }}</template>
      </el-table-column>
      <el-table-column label="毛利" width="110">
        <template #default="{ row }">
          <span :class="row.margin >= 0 ? 'green' : 'red'">¥{{ pointsToYuan(row.margin) }}</span>
        </template>
      </el-table-column>
      <template #empty>暂无数据</template>
    </el-table>
    <div v-if="list.length" class="dim tip">
      合计营收 ¥{{ pointsToYuan(summary.total_cost) }} · 厂商成本 ¥{{ pointsToYuan(summary.total_vendor_cost) }}
      · 毛利 ¥{{ pointsToYuan(summary.total_margin) }}
    </div>
  </el-card>
</template>

<style scoped>
.dim { color: var(--el-text-color-secondary); }
.tip { font-size: 12px; margin-top: 8px; }
.green { color: var(--el-color-success); }
.red { color: var(--el-color-danger); }
</style>
