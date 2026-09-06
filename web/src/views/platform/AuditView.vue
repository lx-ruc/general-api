<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { apiListAudit } from '../../api/platform'
import { fmtTime } from '../../utils/format'

const list = ref<any[]>([])
const total = ref(0)
const loading = ref(false)
const filters = reactive({ page: 1, page_size: 30, path: '' })

async function load() {
  loading.value = true
  try {
    const resp = await apiListAudit(filters)
    list.value = resp.list
    total.value = resp.total
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <el-card shadow="never">
    <template #header>
      <div class="card-header">
        <span>操作审计（管理台全部写操作）</span>
        <el-input v-model="filters.path" placeholder="按路径过滤，如 orgs" clearable style="width: 200px"
          @keyup.enter="filters.page = 1; load()" @clear="filters.page = 1; load()" />
      </div>
    </template>
    <el-table :data="list" size="small" v-loading="loading" empty-text="暂无审计记录">
      <el-table-column prop="id" label="#" width="70" />
      <el-table-column prop="actor_id" label="操作人ID" width="90" align="center" />
      <el-table-column prop="actor" label="角色" width="110" />
      <el-table-column label="操作" width="70">
        <template #default="{ row }">
          <el-tag size="small" :type="row.method === 'DELETE' ? 'danger' : row.method === 'POST' ? 'success' : 'warning'">
            {{ row.method }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="path" label="路径" min-width="200" show-overflow-tooltip />
      <el-table-column label="结果" width="70" align="center">
        <template #default="{ row }">
          <el-tag size="small" :type="row.status < 400 ? 'success' : 'danger'">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="detail" label="请求内容（密码已脱敏）" min-width="240" show-overflow-tooltip />
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
</style>
