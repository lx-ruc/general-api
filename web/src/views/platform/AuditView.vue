<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { apiListAudit, type AuditRow } from '../../api/platform'
import { fmtTime } from '../../utils/format'
import { actionName } from '../../utils/auditActions'

// 操作审计（超级管理员专属）：默认只看平台层（/api/platform 前缀）的管理操作，
// 「全部」切换可见管理台全量写操作（客户层等）。操作者列由后端 JOIN 出用户名；
// 已删除/历史账号回退显示裸 ID。
const list = ref<AuditRow[]>([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({
  page: 1,
  page_size: 30,
  scope: 'platform' as 'platform' | 'all',
  actor: '',
  method: '',
  path: '',
  range: [] as string[],
})

function params() {
  return {
    page: filters.page,
    page_size: filters.page_size,
    scope: filters.scope,
    actor: filters.actor || undefined,
    method: filters.method || undefined,
    path: filters.path || undefined,
    // 日期区间 → unix 秒闭开区间（当日含全天）
    start: filters.range?.[0] ? dayjs(filters.range[0]).unix() : undefined,
    end: filters.range?.[1] ? dayjs(filters.range[1]).add(1, 'day').unix() : undefined,
  }
}

async function load() {
  loading.value = true
  try {
    const resp = await apiListAudit(params())
    list.value = resp.list
    total.value = resp.total
  } finally {
    loading.value = false
  }
}
function search() {
  filters.page = 1
  load()
}
function reset() {
  Object.assign(filters, { scope: 'platform', actor: '', method: '', path: '', range: [] })
  search()
}
onMounted(load)

function operator(row: AuditRow): string {
  return row.username || (row.actor_id ? `#${row.actor_id}` : '—')
}
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header"><span>操作审计</span></div>
    </template>

    <el-form inline class="filters" @submit.prevent>
      <el-form-item label="范围">
        <el-radio-group v-model="filters.scope" size="small" @change="search">
          <el-radio-button value="platform">平台层操作</el-radio-button>
          <el-radio-button value="all">全部</el-radio-button>
        </el-radio-group>
      </el-form-item>
      <el-form-item label="操作者">
        <el-input v-model="filters.actor" placeholder="用户名/姓名" clearable style="width: 140px"
          @keyup.enter="search" @clear="search" />
      </el-form-item>
      <el-form-item label="方法">
        <el-select v-model="filters.method" clearable placeholder="全部" style="width: 100px" @change="search">
          <el-option label="POST" value="POST" />
          <el-option label="PUT" value="PUT" />
          <el-option label="DELETE" value="DELETE" />
        </el-select>
      </el-form-item>
      <el-form-item label="路径">
        <el-input v-model="filters.path" placeholder="如 orgs" clearable style="width: 140px"
          @keyup.enter="search" @clear="search" />
      </el-form-item>
      <el-form-item label="时间">
        <el-date-picker v-model="filters.range" type="daterange" value-format="YYYY-MM-DD"
          start-placeholder="开始" end-placeholder="结束" style="width: 240px" @change="search" />
      </el-form-item>
      <el-form-item>
        <el-button @click="reset">重置</el-button>
      </el-form-item>
    </el-form>

    <el-table :data="list" size="small" v-loading="loading" empty-text="暂无审计记录">
      <el-table-column prop="id" label="#" width="70" />
      <el-table-column label="操作人" width="130">
        <template #default="{ row }">
          {{ operator(row) }}
          <span v-if="row.display_name" class="dim">（{{ row.display_name }}）</span>
        </template>
      </el-table-column>
      <el-table-column label="动作" min-width="150">
        <template #default="{ row }">
          <el-tag size="small" :type="row.method === 'DELETE' ? 'danger' : row.method === 'POST' ? 'success' : 'warning'">
            {{ actionName(row.method, row.path) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="path" label="路径" min-width="190" show-overflow-tooltip />
      <el-table-column label="结果" width="70" align="center">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status < 400 ? 'success' : 'danger'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="detail" label="请求内容（密码已脱敏）" min-width="220" show-overflow-tooltip />
      <el-table-column prop="ip" label="IP" width="120" />
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
.card-header { display: flex; justify-content: space-between; align-items: center; }
.filters { margin-bottom: 4px; }
.dim { color: var(--tg-muted); font-size: 12px; }
</style>
