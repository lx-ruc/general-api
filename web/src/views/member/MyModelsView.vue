<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { apiMyModels } from '../../api/member'
import { fmtPrice, fmtPoints, pointsToYuan } from '../../utils/format'

const data = ref<any>(null)

onMounted(async () => {
  data.value = await apiMyModels()
})
</script>

<template>
  <div v-if="data">
    <el-alert type="success" :closable="false" style="margin-bottom: 16px">
      <template #title>
        我的额度：{{ data.quota_limit == null ? '不限' : `${fmtPoints(data.quota_limit - data.quota_used)} 点（¥${pointsToYuan(data.quota_limit - data.quota_used)}）可用` }}
        <span style="color: #909399; font-size: 12px; margin-left: 8px">
          已用 {{ fmtPoints(data.quota_used) }} 点{{ data.quota_limit != null ? ` / ${fmtPoints(data.quota_limit)} 点` : '' }}
        </span>
      </template>
    </el-alert>

    <el-card shadow="never">
      <template #header>我可用的大模型（由公司管理员授权）</template>
      <el-table :data="data.models">
        <el-table-column prop="name" label="模型名" min-width="160">
          <template #default="{ row }"><code>{{ row.name }}</code></template>
        </el-table-column>
        <el-table-column prop="display_name" label="说明" min-width="160" />
        <el-table-column prop="vendor" label="厂商" width="100" />
        <el-table-column label="输入单价" width="130">
          <template #default="{ row }">{{ fmtPrice(row.input_price) }} /1M tokens</template>
        </el-table-column>
        <el-table-column label="输出单价" width="130">
          <template #default="{ row }">{{ fmtPrice(row.output_price) }} /1M tokens</template>
        </el-table-column>
      </el-table>
      <p style="color: #909399; font-size: 13px">
        计费说明：成本 = 输入 tokens × 输入单价 + 输出 tokens × 输出单价。1 元 = 1,000,000 点。
        前往 <router-link to="/member/docs">接入文档</router-link> 查看调用方式。
      </p>
    </el-card>
  </div>
</template>
