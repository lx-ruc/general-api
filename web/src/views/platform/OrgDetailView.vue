<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { apiGetOrg } from '../../api/platform'
import { fmtTime, fmtPoints, pointsToYuan, roleNames } from '../../utils/format'

const route = useRoute()
const router = useRouter()
const org = ref<any>(null)
const grants = ref<any[]>([])
const users = ref<any[]>([])

onMounted(async () => {
  const resp = await apiGetOrg(Number(route.params.id))
  org.value = resp.org
  grants.value = resp.quota_grants
  users.value = resp.users
})
</script>

<template>
  <div v-if="org">
    <el-page-header title="返回公司列表" @back="router.back()" style="margin-bottom: 16px">
      <template #content>{{ org.name }}</template>
    </el-page-header>

    <el-card shadow="never" style="margin-bottom: 16px">
      <el-descriptions :column="3" border>
        <el-descriptions-item label="公司名">{{ org.name }}</el-descriptions-item>
        <el-descriptions-item label="备注">{{ org.remark || '-' }}</el-descriptions-item>
        <el-descriptions-item label="状态">
          <el-tag :type="org.status === 1 ? 'success' : 'danger'">{{ org.status === 1 ? '启用' : '停用' }}</el-tag>
        </el-descriptions-item>
        <el-descriptions-item label="额度上限">{{ fmtPoints(org.quota_limit) }} 点（¥{{ pointsToYuan(org.quota_limit) }}）</el-descriptions-item>
        <el-descriptions-item label="已消耗">{{ fmtPoints(org.quota_used) }} 点（¥{{ pointsToYuan(org.quota_used) }}）</el-descriptions-item>
        <el-descriptions-item label="剩余">
          <span style="color: #67c23a; font-weight: 600">{{ fmtPoints(org.quota_limit - org.quota_used) }} 点</span>
        </el-descriptions-item>
      </el-descriptions>
    </el-card>

    <el-row :gutter="16">
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>额度流水（最近 50 条）</template>
          <el-table :data="grants" size="small" max-height="400">
            <el-table-column label="时间" width="160">
              <template #default="{ row }">{{ fmtTime(row.created_at) }}</template>
            </el-table-column>
            <el-table-column label="变更" width="120">
              <template #default="{ row }">
                <span :style="{ color: row.amount >= 0 ? '#67c23a' : '#f56c6c' }">
                  {{ row.amount >= 0 ? '+' : '' }}{{ fmtPoints(row.amount) }}
                </span>
              </template>
            </el-table-column>
            <el-table-column prop="remark" label="备注" show-overflow-tooltip />
          </el-table>
        </el-card>
      </el-col>
      <el-col :span="12">
        <el-card shadow="never">
          <template #header>公司账号（只读，管理由公司管理员进行）</template>
          <el-table :data="users" size="small" max-height="400">
            <el-table-column prop="username" label="用户名" />
            <el-table-column prop="display_name" label="姓名" />
            <el-table-column label="角色" width="100">
              <template #default="{ row }">{{ roleNames[row.role] || row.role }}</template>
            </el-table-column>
            <el-table-column label="额度（已用/上限）" width="180">
              <template #default="{ row }">
                {{ fmtPoints(row.quota_used) }} / {{ row.quota_limit == null ? '不限' : fmtPoints(row.quota_limit) }}
              </template>
            </el-table-column>
            <el-table-column label="状态" width="70">
              <template #default="{ row }">
                <el-tag size="small" :type="row.status === 1 ? 'success' : 'danger'">{{ row.status === 1 ? '启用' : '停用' }}</el-tag>
              </template>
            </el-table-column>
          </el-table>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>
