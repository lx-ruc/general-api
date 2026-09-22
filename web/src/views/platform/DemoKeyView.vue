<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { apiListModels } from '../../api/platform'
import { apiConfigureDemoKey, apiGetDemoKey, apiRotateDemoKey, type DemoKeyConfig } from '../../api/demoKey'
import { fmtQuota, fmtTime } from '../../utils/format'
import { copyText } from '../../utils/clipboard'

// 对外演示体验密钥：给来访客户不建账号、直接按文档示例调 /v1 的真实 key。
// 挂专用体验账号（个人不限额），总额度由体验客户额度承担，消耗照常计量。
const cfg = ref<DemoKeyConfig | null>(null)
const loading = ref(false)
const saving = ref(false)

// 授权模型候选（仅启用中的模型）
const modelOptions = ref<string[]>([])

// 有效期表单：永久 / 30 天 / 90 天 / 自定义天数（保存时从当前时刻起算）
const expiryMode = ref<'never' | '30' | '90' | 'custom'>('never')
const customDays = ref(30)
const quotaPoints = ref(10_000_000) // 默认 10M token
const selectedModels = ref<string[]>([])

// 已加载配置导出的有效期签名：保存时仅当用户改动过才提交 expires_at
// （有效期每次保存都从「现在」重新起算，不改动就不该顺延）
let loadedExpirySig = ''

function expirySig(): string {
  return expiryMode.value === 'custom' ? `custom:${customDays.value}` : expiryMode.value
}

function applyCfg(c: DemoKeyConfig) {
  cfg.value = c
  quotaPoints.value = c.org.quota_limit > 0 ? c.org.quota_limit : 10_000_000
  selectedModels.value = [...c.models]
  if (!c.expires_at) {
    expiryMode.value = 'never'
  } else {
    const days = Math.max(1, Math.ceil((c.expires_at - Date.now() / 1000) / 86400))
    if (days === 30) expiryMode.value = '30'
    else if (days === 90) expiryMode.value = '90'
    else {
      expiryMode.value = 'custom'
      customDays.value = days
    }
  }
  loadedExpirySig = expirySig()
}

async function load() {
  loading.value = true
  try {
    applyCfg(await apiGetDemoKey())
  } finally {
    loading.value = false
  }
}

async function loadModels() {
  try {
    const list = await apiListModels()
    modelOptions.value = list.filter((m) => m.status === 1).map((m) => m.name)
  } catch {
    modelOptions.value = [] // 候选拉失败不阻塞页面；保存时后端仍会校验
  }
}

onMounted(() => {
  load()
  loadModels()
})

const usagePct = computed(() => {
  const c = cfg.value
  if (!c || c.org.quota_limit <= 0) return 0
  return Math.min(100, Math.round((c.org.quota_used / c.org.quota_limit) * 100))
})

// 未配额度时体验密钥必被拒（Precheck 对 limit=0 的客户一律 429）
const quotaMissing = computed(
  () => !!cfg.value?.provisioned && cfg.value.org.quota_limit <= 0,
)

async function copyKey() {
  if (!cfg.value?.key) return
  if (await copyText(cfg.value.key)) ElMessage.success('已复制')
  else ElMessage.error('复制失败，请手动选择复制')
}

async function rotate() {
  try {
    await ElMessageBox.confirm(
      '轮换后旧密钥立即失效（正在使用它的访客会立刻 401），确定继续？',
      '轮换体验密钥',
      { confirmButtonText: '轮换', cancelButtonText: '取消', type: 'warning' },
    )
  } catch {
    return
  }
  loading.value = true
  try {
    const r = await apiRotateDemoKey()
    ElMessage.success('已生成新密钥')
    await load()
    if (r.key && (await copyText(r.key))) ElMessage.success('新密钥已复制到剪贴板')
  } finally {
    loading.value = false
  }
}

async function toggleEnabled(v: boolean | string | number) {
  if (!cfg.value) return
  saving.value = true
  try {
    applyCfg(await apiConfigureDemoKey({ enabled: !!v }))
    ElMessage.success(v ? '已启用' : '已停用（密钥立即失效）')
  } finally {
    saving.value = false
  }
}

async function save() {
  saving.value = true
  try {
    const body: Record<string, unknown> = {
      quota_points: quotaPoints.value,
      models: selectedModels.value,
    }
    if (expirySig() !== loadedExpirySig) {
      body.expires_at =
        expiryMode.value === 'never'
          ? 0
          : Math.floor(Date.now() / 1000) +
            (expiryMode.value === 'custom' ? customDays.value : Number(expiryMode.value)) * 86400
    }
    applyCfg(await apiConfigureDemoKey(body))
    ElMessage.success('配置已保存')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div v-loading="loading">
    <el-alert v-if="quotaMissing" type="warning" :closable="false" show-icon style="margin-bottom: 16px"
      title="尚未设置体验总额度"
      description="体验客户额度为 0 时所有调用都会被拒绝（429）。请在下方设置体验总额度并保存。" />

    <el-card shadow="never">
      <template #header>
        <div class="card-header">
          <span>体验密钥</span>
          <div>
            <el-switch v-if="cfg?.provisioned" :model-value="cfg.enabled" :loading="saving"
              @change="toggleEnabled" active-text="启用" inactive-text="停用" style="margin-right: 12px" />
            <el-button type="primary" plain @click="rotate">{{ cfg?.provisioned ? '轮换密钥' : '生成密钥' }}</el-button>
          </div>
        </div>
      </template>

      <template v-if="cfg?.provisioned">
        <div class="key-row">
          <code class="key-text">{{ cfg.key || '（未生成）' }}</code>
          <el-button size="small" @click="copyKey" :disabled="!cfg.key">复制</el-button>
        </div>
        <el-descriptions :column="3" border style="margin-top: 16px">
          <el-descriptions-item label="状态">
            <el-tag :type="cfg.enabled ? 'success' : 'danger'">{{ cfg.enabled ? '启用中' : '已停用' }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="有效期">
            {{ cfg.expires_at ? `至 ${fmtTime(cfg.expires_at)}` : '永久' }}
          </el-descriptions-item>
          <el-descriptions-item label="归属账号">{{ cfg.user.username }}（客户「{{ cfg.org.name }}」）</el-descriptions-item>
        </el-descriptions>
      </template>
      <el-empty v-else description="尚未生成体验密钥——点击「生成密钥」开通" :image-size="72" />

      <el-alert type="info" :closable="false" style="margin-top: 16px"
        title="这枚密钥交给来访客户：无需注册账号，直接按文档示例填 Authorization: Bearer &lt;密钥&gt; 调用 /v1/chat/completions 试用。消耗按模型定价计量，记在体验客户名下。" />
    </el-card>

    <el-card shadow="never" style="margin-top: 16px">
      <template #header><span>体验配置</span></template>
      <el-form label-width="110px" style="max-width: 640px">
        <el-form-item label="体验总额度">
          <el-input-number v-model="quotaPoints" :min="0" :step="1_000_000" style="width: 220px" />
          <div class="hint">当前 {{ fmtQuota(cfg?.org.quota_limit) }}，已耗 {{ fmtQuota(cfg?.org.quota_used) }}</div>
        </el-form-item>
        <el-form-item v-if="cfg?.provisioned" label="用量水位">
          <el-progress :percentage="usagePct" :stroke-width="14"
            :status="usagePct >= 100 ? 'exception' : usagePct >= 80 ? 'warning' : undefined"
            style="width: 360px" />
        </el-form-item>
        <el-form-item label="有效期">
          <el-radio-group v-model="expiryMode">
            <el-radio-button value="never">永久</el-radio-button>
            <el-radio-button value="30">30 天</el-radio-button>
            <el-radio-button value="90">90 天</el-radio-button>
            <el-radio-button value="custom">自定义</el-radio-button>
          </el-radio-group>
          <el-input-number v-if="expiryMode === 'custom'" v-model="customDays" :min="1" :max="3650"
            style="width: 140px; margin-left: 12px" />
          <div class="hint">改动并保存后从当前时刻重新起算；「{{ cfg?.expires_at ? fmtTime(cfg.expires_at) : '永久' }}」之前不变</div>
        </el-form-item>
        <el-form-item label="授权模型">
          <el-select v-model="selectedModels" multiple filterable collapse-tags collapse-tags-tooltip
            placeholder="选择体验密钥可用的模型" style="width: 420px">
            <el-option v-for="m in modelOptions" :key="m" :label="m" :value="m" />
          </el-select>
          <div class="hint">访客可见且可调用的模型范围；消耗按各模型定价计量</div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存配置</el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.key-row {
  display: flex;
  align-items: center;
  gap: 12px;
}
.key-text {
  flex: 1;
  padding: 8px 12px;
  border-radius: 4px;
  background: var(--el-fill-color-light);
  font-size: 14px;
  word-break: break-all;
  user-select: all;
}
.hint {
  width: 100%;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}
</style>
