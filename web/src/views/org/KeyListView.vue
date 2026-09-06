<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { apiOrgKeys, apiUpdateOrgKeyStatus, type OrgKey } from '../../api/org'
import { fmtTime } from '../../utils/format'

const list = ref<OrgKey[]>([])
const total = ref(0)
const query = reactive({ page: 1, page_size: 20 })

async function load() {
  const resp = await apiOrgKeys(query)
  list.value = resp.list
  total.value = resp.total
}
onMounted(load)

async function toggle(k: OrgKey) {
  await apiUpdateOrgKeyStatus(k.id, k.status === 1 ? 0 : 1)
  ElMessage.success(k.status === 1 ? '密钥已禁用' : '密钥已启用')
  load()
}
</script>

<template>
  <el-card shadow="never">
    <template #header>密钥一览（公司内全部员工的 API key）</template>
    <el-table :data="list">
      <el-table-column prop="id" label="#" width="60" />
      <el-table-column prop="username" label="所属员工" width="110" />
      <el-table-column prop="name" label="名称" min-width="120" />
      <el-table-column label="密钥前缀" width="150">
        <template #default="{ row }"><code>{{ row.key_prefix }}…</code></template>
      </el-table-column>
      <el-table-column label="状态" width="80">
        <template #default="{ row }">
          <el-tag :type="row.status === 1 ? 'success' : 'danger'">{{ row.status === 1 ? '启用' : '禁用' }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="最近使用" width="160">
        <template #default="{ row }">{{ fmtTime(row.last_used_at) }}</template>
      </el-table-column>
      <el-table-column label="创建时间" width="160">
        <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="100" fixed="right">
        <template #default="{ row }">
          <el-button size="small" @click="toggle(row)">{{ row.status === 1 ? '禁用' : '启用' }}</el-button>
        </template>
      </el-table-column>
    </el-table>
    <el-pagination style="margin-top: 12px; justify-content: flex-end" layout="total, prev, pager, next"
      :total="total" :page-size="query.page_size" :current-page="query.page"
      @current-change="(p: number) => { query.page = p; load() }" />
  </el-card>
</template>
