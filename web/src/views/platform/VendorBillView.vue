<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { ElMessage } from 'element-plus'
import { apiVendorBills, apiUpsertVendorBill, apiDeleteVendorBill, type VendorDiffRow } from '../../api/platform'
import { fmtPoints, pointsToYuan } from '../../utils/format'

const period = ref(dayjs().format('YYYY-MM'))
const list = ref<VendorDiffRow[]>([])
const loading = ref(false)

const periodOptions = (() => {
  const out: string[] = []
  for (let i = 0; i < 12; i++) out.push(dayjs().subtract(i, 'month').format('YYYY-MM'))
  return out
})()

async function load() {
  loading.value = true
  try {
    const r = await apiVendorBills(period.value)
    list.value = r.list || []
  } finally {
    loading.value = false
  }
}
onMounted(load)

// 录入厂商账单
const billVisible = ref(false)
const billSaving = ref(false)
const billForm = reactive({ channel_id: 0, channel_name: '', billed_points: 0, note: '' })
function openBill(row?: VendorDiffRow) {
  billForm.channel_id = row?.channel_id || 0
  billForm.channel_name = row?.channel_name || ''
  billForm.billed_points = row?.billed_points || 0
  billForm.note = row?.note || ''
  billVisible.value = true
}
async function submitBill() {
  if (!billForm.channel_id || !billForm.billed_points) {
    ElMessage.warning('请选择渠道并填写厂商账单金额')
    return
  }
  billSaving.value = true
  try {
    await apiUpsertVendorBill(period.value, billForm.channel_id, billForm.billed_points, billForm.note)
    ElMessage.success('已保存')
    billVisible.value = false
    load()
  } finally {
    billSaving.value = false
  }
}

async function removeBill(row: VendorDiffRow) {
  if (!row.bill_id) return
  await apiDeleteVendorBill(row.bill_id)
  ElMessage.success('已删除录入')
  load()
}

function rowClass({ row }: { row: VendorDiffRow }): string {
  return row.over_pct ? 'diff-red' : ''
}
</script>

<template>
  <el-card shadow="never" v-loading="loading">
    <template #header>
      <div class="card-header">
        <span>厂商账单对账<span class="dim">（我方 Σ厂商成本 vs 厂商账单，偏差 &gt;2% 标红）</span></span>
        <div>
          <el-select v-model="period" style="width: 120px; margin-right: 8px" @change="load">
            <el-option v-for="p in periodOptions" :key="p" :label="p" :value="p" />
          </el-select>
          <el-button type="primary" @click="openBill()">录入账单</el-button>
        </div>
      </div>
    </template>

    <el-table :data="list" :row-class-name="rowClass" empty-text="该账期暂无渠道用量。录入厂商账单后自动比对差异。">
      <el-table-column prop="channel_name" label="渠道" min-width="120">
        <template #default="{ row }">
          {{ row.channel_name || `#${row.channel_id}` }}
        </template>
      </el-table-column>
      <el-table-column prop="requests" label="请求数" width="90" align="right" />
      <el-table-column label="我方厂商成本" width="140" align="right">
        <template #default="{ row }">
          <span class="num">{{ fmtPoints(row.our_cost) }}</span>
          <span class="dim"> · ¥{{ pointsToYuan(row.our_cost) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="不计量笔数" width="100" align="right">
        <template #default="{ row }">
          <span :class="{ warn: row.no_usage_count > 0 }">{{ row.no_usage_count }}</span>
        </template>
      </el-table-column>
      <el-table-column label="厂商账单" width="140" align="right">
        <template #default="{ row }">
          <span v-if="row.has_bill" class="num">{{ fmtPoints(row.billed_points) }}</span>
          <span v-else class="dim">未录入</span>
        </template>
      </el-table-column>
      <el-table-column label="差异（我方−账单）" width="140" align="right">
        <template #default="{ row }">
          <span v-if="row.has_bill" class="num">{{ row.diff >= 0 ? '+' : '' }}{{ fmtPoints(row.diff) }}</span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column label="偏差率" width="90" align="right">
        <template #default="{ row }">
          <span v-if="row.has_bill" :class="{ red: row.over_pct }">{{ row.diff_pct }}%</span>
          <span v-else class="dim">—</span>
        </template>
      </el-table-column>
      <el-table-column prop="note" label="备注" min-width="110" show-overflow-tooltip />
      <el-table-column label="操作" width="130" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="openBill(row)">录入/改</el-button>
          <el-button v-if="row.bill_id" size="small" type="danger" link @click="removeBill(row)">删</el-button>
        </template>
      </el-table-column>
    </el-table>
    <div class="dim tip">
      口径：我方厂商成本 = usage_logs.vendor_cost 按渠道按账期汇总（结算快照）；不计量笔数 = 上游未返回 usage 的请求数（我方记 0、厂商可能实收，为潜在漏损）。
    </div>
  </el-card>

  <el-dialog v-model="billVisible" :title="`录入厂商账单：${billForm.channel_name || ''}`" width="440px">
    <el-form label-width="110px">
      <el-form-item label="渠道 ID">
        <el-input-number v-model="billForm.channel_id" :min="1" :controls="false" style="width: 140px" />
      </el-form-item>
      <el-form-item label="账期">{{ period }}</el-form-item>
      <el-form-item label="账单金额（点）">
        <el-input-number v-model="billForm.billed_points" :min="1" :step="1000000" />
        <span class="dim">= ¥{{ pointsToYuan(billForm.billed_points) }}</span>
      </el-form-item>
      <el-form-item label="备注"><el-input v-model="billForm.note" placeholder="如：厂商账单编号" /></el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="billVisible = false">取消</el-button>
      <el-button type="primary" :loading="billSaving" @click="submitBill">保存</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.card-header { display: flex; justify-content: space-between; align-items: center; }
.dim { color: var(--el-text-color-secondary); font-size: 12px; }
.tip { font-size: 12px; margin-top: 8px; }
.num { font-variant-numeric: tabular-nums; }
.warn { color: var(--el-color-warning); }
.red { color: var(--el-color-danger); font-weight: 600; }
:deep(.diff-red) { background: var(--el-color-danger-light-9); }
</style>
