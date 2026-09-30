<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { apiResetPassword } from '../api/auth'
import SiteTopBar from '../components/SiteTopBar.vue'

const route = useRoute()
const router = useRouter()

const token = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const errorMsg = ref('')
const done = ref(false)

onMounted(() => {
  token.value = String(route.query.token || '')
  // 无 token 多为链接被邮件客户端截断/复制不完整，直接提示重新获取
  if (!token.value) errorMsg.value = '重置链接无效：缺少令牌，请重新获取重置邮件'
})

async function submit() {
  errorMsg.value = ''
  if (!token.value) {
    errorMsg.value = '重置链接无效：缺少令牌，请重新获取重置邮件'
    return
  }
  if (newPassword.value.length < 6) {
    errorMsg.value = '新密码至少 6 位'
    return
  }
  if (newPassword.value !== confirmPassword.value) {
    errorMsg.value = '两次输入的密码不一致'
    return
  }
  loading.value = true
  try {
    await apiResetPassword(token.value, newPassword.value)
    done.value = true
  } catch (e: unknown) {
    errorMsg.value = e instanceof Error ? e.message : '重置失败，请重试'
  } finally {
    loading.value = false
  }
}

function backLogin() {
  router.push('/login')
}
</script>

<template>
  <div class="reset">
    <SiteTopBar />
    <div class="reset-body">
      <section class="form-wrap">
        <!-- 成功态：只剩提示与回登录 -->
        <div v-if="done" class="form done-box">
          <h2 class="form-title">密码已重置</h2>
          <p class="done-text">新密码已生效，请使用新密码登录。</p>
          <button class="submit" type="button" @click="backLogin">回登录页</button>
        </div>

        <form v-else class="form" @submit.prevent="submit">
          <h2 class="form-title">设置新密码</h2>
          <p class="form-lead">通过邮件重置链接进入。新密码设置成功后，链接立即失效。</p>

          <label class="field">
            <span class="field-label">新密码（至少 6 位）</span>
            <input v-model="newPassword" class="field-input" type="password"
              autocomplete="new-password" placeholder="请输入新密码" />
          </label>

          <label class="field">
            <span class="field-label">确认新密码</span>
            <input v-model="confirmPassword" class="field-input" type="password"
              autocomplete="new-password" placeholder="请再次输入新密码" />
          </label>

          <p v-if="errorMsg" class="form-error" role="alert">{{ errorMsg }}</p>

          <button class="submit" type="submit" :disabled="loading">
            {{ loading ? '正在提交…' : '重置密码' }}
          </button>

          <div class="form-links">
            <button type="button" class="link" @click="backLogin">返回登录</button>
          </div>
        </form>
      </section>
    </div>
  </div>
</template>

<style scoped>
.reset {
  min-height: 100vh;
  min-height: 100dvh;
  background: var(--tg-paper);
  display: flex;
  flex-direction: column;
  color: var(--tg-ink);
}

/* 与登录页同款右栏白面板，整块居中（本页无看板） */
.reset-body { flex: 1; display: flex; justify-content: center; }
.form-wrap {
  width: 460px; max-width: 100%; box-sizing: border-box;
  margin: 6vh 24px 40px;
  background: var(--tg-surface);
  border: 1px solid var(--tg-line);
  border-radius: 12px;
  display: flex;
  padding: 40px 44px;
}

.form { width: 100%; max-width: 340px; margin: auto; }
.form-title { margin: 0 0 8px; font-size: 20px; font-weight: 600; color: var(--tg-ink); }
.form-lead { margin: 0 0 22px; font-size: 12.5px; color: var(--tg-graphite); line-height: 1.7; }

.field { display: block; margin-bottom: 16px; }
.field-label {
  display: block; font-size: 12.5px; color: var(--tg-graphite);
  margin-bottom: 6px;
}
.field-input {
  width: 100%; box-sizing: border-box;
  border: 1px solid var(--tg-line-strong);
  border-radius: 7px;
  padding: 10px 12px;
  font-size: 14px; color: var(--tg-ink);
  background: var(--tg-surface);
  transition: border-color 0.15s, box-shadow 0.15s;
}
.field-input::placeholder { color: #a8bab2; }
.field-input:focus {
  outline: none;
  border-color: var(--tg-green);
  box-shadow: 0 0 0 3px rgba(18, 164, 98, 0.14);
}

.form-error {
  margin: 0 0 12px; font-size: 12.5px; color: var(--tg-red);
  background: #fdf1f0; border: 1px solid #f3d6d3;
  border-radius: 6px; padding: 7px 10px;
}

.submit {
  width: 100%;
  border: none; border-radius: 7px;
  background: var(--tg-green); color: var(--tg-btn-ink);
  font-size: 14.5px; font-weight: 600; letter-spacing: 0.35em; text-indent: 0.35em;
  padding: 11px 0; cursor: pointer;
  transition: background 0.15s, transform 0.1s;
}
.submit:hover { background: var(--tg-green-hi); }
.submit:active { transform: translateY(1px); background: var(--tg-green-deep); }
.submit:disabled { opacity: 0.6; cursor: default; }
.submit:focus-visible { outline: 2px solid var(--tg-green-deep); outline-offset: 2px; }

.form-links { margin-top: 12px; text-align: center; }
.link {
  background: none; border: none; cursor: pointer;
  font-size: 12.5px; color: var(--tg-green-ink);
}
.link:hover { text-decoration: underline; }

.done-box { text-align: center; }
.done-text { margin: 0 0 20px; font-size: 13.5px; color: var(--tg-graphite); }

@media (max-width: 560px) {
  .form-wrap { margin: 4vh 16px 24px; padding: 28px 22px; }
}
</style>
