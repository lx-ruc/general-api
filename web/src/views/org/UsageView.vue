<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { apiOrgUsage, apiListMembers } from '../../api/org'
import { fmtTime, fmtQuota } from '../../utils/format'

const list = ref<any[]>([])
const total = ref(0)
const members = ref<{ id: number; username: string }[]>([])
const loading = ref(false)
const filters = reactive({
  page: 1, page_size: 20, model: '', status: '', user_id: '', start_date: '', end_date: '',
})

async function load() {
  loading.value = true
  try {
    await doLoad()
  } finally {
    loading.value = false
  }
}
async function doLoad() {
  const params: any = { page: filters.page, page_size: filters.page_size }
  if (filters.model) params.model = filters.model
  if (filters.status) params.status = filters.status
  if (filters.user_id) params.user_id = filters.user_id
  if (filters.start_date) params.start = new Date(filters.start_date + 'T00:00:00').getTime() / 1000
  if (filters.end_date) params.end = new Date(filters.end_date + 'T23:59:59').getTime() / 1000
  const resp = await apiOrgUsage(params)
  list.value = resp.list
  total.value = resp.total
}
onMounted(async () => {
  load()
  const resp = await apiListMembers({ page: 1, page_size: 100 })
  members.value = resp.list
})

function reset() {
  Object.assign(filters, { page: 1, model: '', status: '', user_id: '', start_date: '', end_date: '' })
  load()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>调用日志（本客户）</template>
    <el-form inline style="margin-bottom: 8px">
      <el-form-item label="子账号">
        <el-select v-model="filters.user_id" clearable placeholder="全部" style="width: 150px">
          <el-option v-for="m in members" :key="m.id" :label="m.username" :value="m.id" />
        </el-select>
      </el-form-item>
      <el-form-item label="模型"><el-input v-model="filters.model" style="width: 150px" /></el-form-item>
      <el-form-item label="状态">
        <el-select v-model="filters.status" clearable placeholder="全部" style="width: 120px">
          <el-option label="成功(2xx)" value="200" />
          <el-option label="4xx" value="400" />
          <el-option label="429 限流/额度" value="429" />
          <el-option label="5xx" value="500" />
        </el-select>
      </el-form-item>
      <el-form-item label="日期">
        <el-date-picker v-model="filters.start_date" type="date" value-format="YYYY-MM-DD" placeholder="开始" style="width: 140px" />
        <span style="margin: 0 4px">~</span>
        <el-date-picker v-model="filters.end_date" type="date" value-format="YYYY-MM-DD" placeholder="结束" style="width: 140px" />
      </el-form-item>
      <el-form-item>
        <el-button type="primary" @click="filters.page = 1; load()">查询</el-button>
        <el-button @click="reset">重置</el-button>
      </el-form-item>
    </el-form>

    <el-table :data="list" size="small" v-loading="loading" empty-text="本客户还没有调用记录。">
      <el-table-column prop="id" label="#" width="70" />
      <el-table-column prop="username" label="用户" width="100" />
      <el-table-column prop="model_name" label="模型" width="130" />
      <el-table-column label="流式" width="60">
        <template #default="{ row }">{{ row.is_stream ? '是' : '否' }}</template>
      </el-table-column>
      <el-table-column label="tokens（入 / 出）" width="130" align="right">
        <template #default="{ row }"><span class="num">{{ row.prompt_tokens }} / {{ row.completion_tokens }}</span></template>
      </el-table-column>
      <el-table-column label="扣减额度" width="110" align="right">
        <template #default="{ row }">
          <span v-if="row.cache_hit" class="num green">0 <el-tag size="small" type="success" effect="plain">缓存</el-tag></span>
          <span v-else-if="row.no_usage" class="warn">未计量</span>
          <span v-else class="num green">{{ fmtQuota(row.cost) }}</span>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status < 400 ? 'success' : row.status < 500 ? 'warning' : 'danger'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="error" label="错误" min-width="140" show-overflow-tooltip />
      <el-table-column label="时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="total" :page-size="filters.page_size" :current-page="filters.page"
      @current-change="(p: number) => { filters.page = p; load() }" />
  </el-card>
</template>

<style scoped>
.warn { color: var(--tg-amber); font-size: 12px; }
.green { color: var(--tg-green-ink); }
</style>
