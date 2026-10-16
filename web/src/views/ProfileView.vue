<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../stores/auth'
import { apiSendEmailCode, apiBindEmail } from '../api/auth'
import { roleLabel as roleLabelOf, fmtTime } from '../utils/format'
import { useCountdown } from '../composables/useCountdown'

// 个人资料（三角色通用）：账号信息 + 邮箱绑定/换绑。
// 修改密码在顶栏下拉里已有弹窗，这里不重复。绑定邮箱后即可在登录页
// 「忘记密码」用邮件链接自助重置密码——这是三级账号找回密码的唯一自助通道。

const auth = useAuthStore()
onMounted(() => { auth.fetchMe().catch(() => { /* 401 由拦截器跳登录 */ }) })

const user = computed(() => auth.user)
const roleLabel = computed(() => roleLabelOf(user.value))
const orgLabel = computed(() =>
  user.value?.role === 'platform_admin' ? '平台' : (user.value?.org_name || '—'))
const boundEmail = computed(() => user.value?.email || '')

// ---- 绑定/换绑表单 ----
const emailInput = ref('')
const codeInput = ref('')
const sending = ref(false)
const binding = ref(false)
const errorMsg = ref('')
const infoMsg = ref('')
const { countdown, start } = useCountdown(60)

const emailValid = computed(() => /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(emailInput.value.trim()))

async function sendCode() {
  const email = emailInput.value.trim()
  if (!email) {
    errorMsg.value = '请先填写邮箱'
    return
  }
  if (!emailValid.value) {
    errorMsg.value = '邮箱格式不正确'
    return
  }
  sending.value = true
  errorMsg.value = ''
  infoMsg.value = ''
  try {
    const r = await apiSendEmailCode(email)
    infoMsg.value = r.dev_code
      ? `SMTP 未配置（开发模式），验证码：${r.dev_code}`
      : '验证码已发送，请查收邮箱（注意垃圾邮件箱）'
    start()
  } catch (e: unknown) {
    errorMsg.value = e instanceof Error ? e.message : '发送失败，请重试'
  } finally {
    sending.value = false
  }
}

async function submitBind() {
  const email = emailInput.value.trim()
  if (!email || !codeInput.value.trim()) {
    errorMsg.value = '请填写邮箱和验证码'
    return
  }
  binding.value = true
  errorMsg.value = ''
  try {
    await apiBindEmail(email, codeInput.value.trim())
    ElMessage.success('邮箱绑定成功，可在登录页「忘记密码」自助重置密码')
    emailInput.value = ''
    codeInput.value = ''
    infoMsg.value = ''
    await auth.fetchMe()
  } catch (e: unknown) {
    errorMsg.value = e instanceof Error ? e.message : '绑定失败，请重试'
  } finally {
    binding.value = false
  }
}
</script>

<template>
  <div v-if="user" class="profile">
    <el-card shadow="never" class="card">
      <template #header>账号信息</template>
      <el-descriptions :column="2" border>
        <el-descriptions-item label="用户名">{{ user.username }}</el-descriptions-item>
        <el-descriptions-item label="显示名">{{ user.display_name || '—' }}</el-descriptions-item>
        <el-descriptions-item label="角色">{{ roleLabel }}</el-descriptions-item>
        <el-descriptions-item label="所属客户">{{ orgLabel }}</el-descriptions-item>
        <el-descriptions-item label="绑定邮箱">
          <span v-if="boundEmail" class="bound">{{ boundEmail }}</span>
          <span v-else class="none">未绑定</span>
        </el-descriptions-item>
        <el-descriptions-item label="最近登录">{{ fmtTime(user.last_login_at) }}</el-descriptions-item>
      </el-descriptions>
    </el-card>

    <el-card shadow="never" class="card">
      <template #header>{{ boundEmail ? '换绑邮箱' : '绑定邮箱' }}</template>
      <p class="hint">
        绑定邮箱后，可在登录页「忘记密码」通过邮件链接自助重置密码，无需联系管理员。
        换绑需验证新邮箱；换绑后旧邮箱里未使用的重置链接立即失效。
      </p>
      <el-form label-width="90px" class="form" @submit.prevent="submitBind">
        <el-form-item label="新邮箱">
          <div class="with-btn">
            <el-input v-model="emailInput" type="email" spellcheck="false"
              placeholder="name@company.com" @input="errorMsg = ''" />
            <el-button :disabled="sending || countdown > 0" @click="sendCode">
              {{ countdown > 0 ? `${countdown}s` : sending ? '发送中…' : '发验证码' }}
            </el-button>
          </div>
        </el-form-item>
        <el-form-item label="验证码">
          <el-input v-model="codeInput" maxlength="6" placeholder="6 位数字"
            style="width: 180px" @input="errorMsg = ''" />
        </el-form-item>
        <el-form-item v-if="infoMsg || errorMsg">
          <p v-if="infoMsg" class="msg info">{{ infoMsg }}</p>
          <p v-if="errorMsg" class="msg error" role="alert">{{ errorMsg }}</p>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="binding" @click="submitBind">
            {{ boundEmail ? '确认换绑' : '确认绑定' }}
          </el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<style scoped>
.profile { max-width: 720px; display: flex; flex-direction: column; gap: 16px; }
.card { border-radius: 10px; }
.hint { margin: 0 0 14px; font-size: 12.5px; color: var(--tg-muted, #8da098); line-height: 1.7; }
.with-btn { display: flex; gap: 8px; width: 100%; }
.bound { color: var(--tg-green-ink, #0e7d54); font-weight: 600; }
.none { color: var(--tg-muted, #8da098); }
.msg { margin: 0; font-size: 12.5px; border-radius: 6px; padding: 7px 10px; }
.info {
  color: var(--tg-green-ink, #0e7d54);
  background: var(--tg-green-wash, #e9f7ef);
  border: 1px solid var(--tg-green-wash-strong, #d3efdf);
}
.error { color: #c0403a; background: #fdf1f0; border: 1px solid #f3d6d3; }
</style>
