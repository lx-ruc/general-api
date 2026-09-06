<script setup lang="ts">
import { onBeforeUnmount, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import http from '../api/http'

const router = useRouter()
const loading = ref(false)
const sending = ref(false)
const errorMsg = ref('')
const infoMsg = ref('')
const countdown = ref(0)
let timer: ReturnType<typeof setInterval> | undefined

const form = reactive({
  org_name: '', email: '', code: '', username: '', password: '', confirm: '',
})

onBeforeUnmount(() => clearInterval(timer))

async function sendCode() {
  if (!form.email) {
    errorMsg.value = '请先填写邮箱'
    return
  }
  sending.value = true
  errorMsg.value = ''
  try {
    const r = await http.post<any, any>('/api/auth/send-code', { email: form.email })
    infoMsg.value = r.dev_code
      ? `SMTP 未配置（开发模式），验证码：${r.dev_code}`
      : '验证码已发送，请查收邮箱（注意垃圾邮件箱）'
    countdown.value = 60
    timer = setInterval(() => {
      countdown.value--
      if (countdown.value <= 0) clearInterval(timer)
    }, 1000)
  } catch {
    /* 拦截器已提示 */
  } finally {
    sending.value = false
  }
}

async function submit() {
  if (!form.org_name || !form.email || !form.code || !form.username || !form.password) {
    errorMsg.value = '请填写全部字段'
    return
  }
  if (form.password !== form.confirm) {
    errorMsg.value = '两次输入的密码不一致'
    return
  }
  loading.value = true
  errorMsg.value = ''
  try {
    await http.post<any, any>('/api/auth/register', {
      org_name: form.org_name, email: form.email, code: form.code,
      username: form.username, password: form.password,
    })
    router.push('/login')
  } catch {
    /* 拦截器已提示 */
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="register">
    <div class="wrap">
      <button class="back" type="button" @click="router.push('/login')">
        ← 返回登录
      </button>

      <form class="form" @submit.prevent="submit">
        <h1 class="title">注册公司</h1>
        <p class="hint">注册后你将成为该公司的管理员，可管理员工、模型授权与额度</p>

        <label class="field">
          <span class="label">公司名称</span>
          <input v-model="form.org_name" class="input" type="text" placeholder="如：星河科技" />
        </label>

        <label class="field">
          <span class="label">邮箱</span>
          <span class="with-btn">
            <input v-model="form.email" class="input" type="email" autocomplete="email"
              spellcheck="false" placeholder="name@company.com" />
            <button class="code-btn" type="button" :disabled="sending || countdown > 0" @click="sendCode">
              {{ countdown > 0 ? `${countdown}s` : sending ? '发送中…' : '获取验证码' }}
            </button>
          </span>
        </label>

        <label class="field">
          <span class="label">邮箱验证码</span>
          <input v-model="form.code" class="input code-input" type="text" inputmode="numeric"
            maxlength="6" placeholder="6 位数字" />
        </label>

        <label class="field">
          <span class="label">管理员账号</span>
          <input v-model="form.username" class="input" type="text" autocomplete="username"
            spellcheck="false" placeholder="至少 3 位，登录用" />
        </label>

        <label class="field">
          <span class="label">密码</span>
          <input v-model="form.password" class="input" type="password" autocomplete="new-password"
            placeholder="至少 6 位" />
        </label>

        <label class="field">
          <span class="label">确认密码</span>
          <input v-model="form.confirm" class="input" type="password" autocomplete="new-password"
            placeholder="再输入一次" />
        </label>

        <p v-if="infoMsg" class="info">{{ infoMsg }}</p>
        <p v-if="errorMsg" class="error" role="alert">{{ errorMsg }}</p>

        <button class="submit" type="submit" :disabled="loading">
          {{ loading ? '正在注册…' : '注 册' }}
        </button>

        <p class="note">注册即代表同意由平台管理员为你的公司分配调用额度（初始额度为 0）</p>
      </form>
    </div>
  </div>
</template>

<style scoped>
.register {
  min-height: 100vh;
  background:
    radial-gradient(1100px 520px at 10% -12%, rgba(18, 164, 98, 0.10), transparent 62%),
    var(--tg-paper);
  display: flex; align-items: center; justify-content: center;
  padding: 32px 20px;
}
.wrap { width: 100%; max-width: 440px; }
.back {
  background: none; border: none; cursor: pointer;
  color: var(--tg-graphite); font-size: 13px; margin-bottom: 12px; padding: 0;
}
.back:hover { color: var(--tg-green-ink); }

.form {
  background: var(--tg-surface);
  border: 1px solid var(--tg-line);
  border-radius: 12px;
  padding: 28px 28px 24px;
  box-shadow: 0 12px 32px rgba(27, 43, 36, 0.07);
}
.title { margin: 0 0 4px; font-size: 20px; font-weight: 600; color: var(--tg-ink); }
.hint { margin: 0 0 20px; font-size: 12.5px; color: var(--tg-muted); }

.field { display: block; margin-bottom: 14px; }
.label { display: block; font-size: 12.5px; color: var(--tg-graphite); margin-bottom: 6px; }
.input {
  width: 100%; box-sizing: border-box;
  border: 1px solid var(--tg-line-strong); border-radius: 7px;
  padding: 9px 12px; font-size: 14px; color: var(--tg-ink);
  background: var(--tg-surface);
  transition: border-color 0.15s, box-shadow 0.15s;
}
.input::placeholder { color: #a8bab2; }
.input:focus { outline: none; border-color: var(--tg-green); box-shadow: 0 0 0 3px rgba(18, 164, 98, 0.14); }

.with-btn { display: flex; gap: 8px; }
.code-btn {
  flex: none; border: 1px solid var(--tg-green); border-radius: 7px;
  background: var(--tg-green-wash); color: var(--tg-green-ink);
  font-size: 13px; padding: 0 14px; cursor: pointer; white-space: nowrap;
  transition: background 0.15s;
}
.code-btn:hover:not(:disabled) { background: var(--tg-green-wash-strong); }
.code-btn:disabled { opacity: 0.55; cursor: default; }
.code-input { letter-spacing: 0.4em; }

.info {
  margin: 0 0 12px; font-size: 12.5px; color: var(--tg-green-ink);
  background: var(--tg-green-wash); border: 1px solid var(--tg-green-wash-strong);
  border-radius: 6px; padding: 7px 10px;
}
.error {
  margin: 0 0 12px; font-size: 12.5px; color: var(--tg-red);
  background: #fdf1f0; border: 1px solid #f3d6d3;
  border-radius: 6px; padding: 7px 10px;
}

.submit {
  width: 100%; border: none; border-radius: 7px;
  background: var(--tg-green); color: var(--tg-btn-ink);
  font-size: 14.5px; font-weight: 600; letter-spacing: 0.35em; text-indent: 0.35em;
  padding: 11px 0; cursor: pointer;
  transition: background 0.15s, transform 0.1s;
}
.submit:hover { background: var(--tg-green-hi); }
.submit:active { transform: translateY(1px); }
.submit:disabled { opacity: 0.6; cursor: default; }

.note { margin: 14px 0 0; font-size: 11.5px; color: var(--tg-muted); text-align: center; }
</style>
