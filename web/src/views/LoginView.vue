<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore, homeOf } from '../stores/auth'

const router = useRouter()
const auth = useAuthStore()
const loading = ref(false)
const username = ref('')
const password = ref('')
const errorMsg = ref('')

// ---- 计量条：token 流动读数 ----
const relayed = ref(1_283_905)
const perSec = ref(642)
let timer: ReturnType<typeof setInterval> | undefined

const STREAM = [
  'deepseek-chat', 'glm-4.5', 'qwen-max', 'deepseek-reasoner',
  'qwen-plus', 'glm-4.5-air', 'qwen-turbo', 'glm-4-flash',
]

onMounted(() => {
  if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    timer = setInterval(() => {
      relayed.value += Math.round(perSec.value * (0.7 + Math.random() * 0.6))
    }, 1000)
  }
})
onBeforeUnmount(() => clearInterval(timer))

async function submit() {
  if (!username.value || !password.value) {
    errorMsg.value = '请输入用户名和密码'
    return
  }
  loading.value = true
  errorMsg.value = ''
  try {
    await auth.signIn(username.value, password.value)
    router.push(homeOf(auth.user?.role))
  } catch {
    errorMsg.value = '用户名或密码不正确，请重试'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login">
    <main class="panel-area">
      <!-- 品牌与计量 -->
      <section class="identity">
        <div class="brand-row">
          <span class="brand-dot" aria-hidden="true"></span>
          <span class="brand-name">token 中转站</span>
        </div>
        <h1 class="headline">
          国产大模型 API<br />
          统一接入，按点计费
        </h1>
        <p class="sub">
          厂商渠道收口为一个 OpenAI 兼容端点；每次调用的 token 都被计量，
          从公司池与员工额度同步结算。
        </p>

        <!-- token 流动计量条：主题装置 -->
        <figure class="meter-band" aria-hidden="true">
          <div class="stream-mask">
            <div class="stream">
              <span v-for="(m, i) in [...STREAM, ...STREAM]" :key="i" class="stream-chip">
                {{ m }}<span class="stream-caret">▸</span>
              </span>
            </div>
          </div>
          <figcaption class="meter-read">
            <span class="num relayed">{{ relayed.toLocaleString('en-US') }}</span>
            <span class="meter-unit">tokens 已中转</span>
            <span class="num meter-rate">{{ perSec }}/s</span>
            <span class="meter-ticks"><i v-for="n in 24" :key="n"></i></span>
          </figcaption>
        </figure>
      </section>

      <!-- 登录 -->
      <section class="form-wrap">
        <form class="form" @submit.prevent="submit">
          <h2 class="form-title">登录管理台</h2>
          <p class="form-hint">平台管理员 · 公司管理员 · 员工</p>

          <label class="field">
            <span class="field-label">用户名</span>
            <input v-model="username" class="field-input" type="text" autocomplete="username"
              spellcheck="false" placeholder="如 admin" />
          </label>

          <label class="field">
            <span class="field-label">密码</span>
            <input v-model="password" class="field-input" type="password" autocomplete="current-password"
              placeholder="请输入密码" />
          </label>

          <p v-if="errorMsg" class="form-error" role="alert">{{ errorMsg }}</p>

          <button class="submit" type="submit" :disabled="loading">
            {{ loading ? '正在登录…' : '登 录' }}
          </button>
        </form>
        <p class="form-foot mono">POST /api/auth/login → JWT · 有效期 12h</p>
      </section>
    </main>
  </div>
</template>

<style scoped>
.login {
  min-height: 100vh;
  background:
    radial-gradient(1100px 500px at 12% -10%, rgba(11, 132, 85, 0.18), transparent 60%),
    var(--tg-sidebar);
  display: flex; align-items: center; justify-content: center;
  padding: 32px 20px;
  color: #e8f0ec;
}

.panel-area {
  width: 100%; max-width: 980px;
  display: grid; grid-template-columns: 1.25fr 340px; gap: 56px;
  align-items: center;
}
/* grid item min-width 陷阱：让走马灯宽度不向外传播，由 stream-mask 裁切 */
.identity { min-width: 0; }
.form-wrap { min-width: 0; }
@media (max-width: 860px) {
  .panel-area { grid-template-columns: 1fr; gap: 36px; max-width: 420px; }
}

/* ---------- 品牌区 ---------- */
.brand-row { display: flex; align-items: center; gap: 9px; margin-bottom: 20px; }
.brand-dot {
  width: 9px; height: 9px; border-radius: 50%;
  background: var(--tg-green);
  box-shadow: 0 0 0 4px rgba(11, 132, 85, 0.2);
}
.brand-name { font-size: 14px; font-weight: 600; letter-spacing: 0.04em; color: #cfe0d8; }

.headline {
  font-size: clamp(30px, 4.2vw, 44px);
  line-height: 1.22; font-weight: 700;
  margin: 0 0 16px; letter-spacing: 0.01em;
  animation: rise 0.6s cubic-bezier(0.2, 0.8, 0.3, 1) both;
}
.sub {
  font-size: 14px; line-height: 1.9; color: #a9c2b6;
  max-width: 34em; margin: 0 0 32px;
  animation: rise 0.6s 0.08s cubic-bezier(0.2, 0.8, 0.3, 1) both;
}
@keyframes rise {
  from { opacity: 0; transform: translateY(10px); }
  to { opacity: 1; transform: none; }
}

/* ---------- 计量条 ---------- */
.meter-band {
  margin: 0;
  border: 1px solid rgba(255, 255, 255, 0.09);
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.03);
  overflow: hidden;
  animation: rise 0.6s 0.16s cubic-bezier(0.2, 0.8, 0.3, 1) both;
}
.stream-mask {
  overflow: hidden; border-bottom: 1px solid rgba(255, 255, 255, 0.09); padding: 12px 0;
  -webkit-mask-image: linear-gradient(90deg, transparent, #000 8%, #000 92%, transparent);
  mask-image: linear-gradient(90deg, transparent, #000 8%, #000 92%, transparent);
}
.stream {
  display: flex; gap: 22px; white-space: nowrap; width: max-content;
  animation: flow 26s linear infinite;
  padding-left: 22px;
}
@keyframes flow { to { transform: translateX(-50%); } }
.stream-chip {
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
  font-size: 12.5px; color: #c2d6cc;
}
.stream-caret { color: var(--tg-green); margin-left: 10px; }

.meter-read {
  display: flex; align-items: baseline; gap: 10px;
  padding: 12px 16px;
}
.relayed { font-size: 22px; font-weight: 600; color: #ffffff; font-variant-numeric: tabular-nums; }
.meter-unit { font-size: 12px; color: #8fa79d; }
.meter-rate { font-size: 12px; color: #58b98c; }
.meter-ticks { margin-left: auto; display: flex; gap: 3px; align-self: center; }
.meter-ticks i {
  width: 2px; height: 10px; border-radius: 1px;
  background: rgba(255, 255, 255, 0.22);
}
.meter-ticks i:nth-child(4n + 1) { height: 14px; background: rgba(255, 255, 255, 0.45); }

/* ---------- 登录表单 ---------- */
.form-wrap { animation: rise 0.6s 0.22s cubic-bezier(0.2, 0.8, 0.3, 1) both; }
.form {
  background: var(--tg-surface);
  border-radius: 12px;
  padding: 30px 28px 26px;
  box-shadow: 0 24px 60px rgba(4, 15, 12, 0.45);
}
.form-title { margin: 0 0 4px; font-size: 19px; font-weight: 600; color: var(--tg-ink); }
.form-hint { margin: 0 0 22px; font-size: 12.5px; color: var(--tg-muted); }

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
.field-input::placeholder { color: #a8b5b0; }
.field-input:focus {
  outline: none;
  border-color: var(--tg-green);
  box-shadow: 0 0 0 3px rgba(11, 132, 85, 0.14);
}

.form-error {
  margin: 0 0 12px; font-size: 12.5px; color: var(--tg-red);
  background: #fdf1f0; border: 1px solid #f3d6d3;
  border-radius: 6px; padding: 7px 10px;
}

.submit {
  width: 100%;
  border: none; border-radius: 7px;
  background: var(--tg-green); color: #ffffff;
  font-size: 14.5px; font-weight: 600; letter-spacing: 0.35em; text-indent: 0.35em;
  padding: 11px 0; cursor: pointer;
  transition: background 0.15s, transform 0.1s;
}
.submit:hover { background: var(--tg-green-deep); }
.submit:active { transform: translateY(1px); }
.submit:disabled { opacity: 0.6; cursor: default; }
.submit:focus-visible { outline: 2px solid #ffffff; outline-offset: 2px; }

.form-foot {
  margin: 9px 0 0; text-align: center;
  font-size: 11px; color: #5c6e67;
}

@media (prefers-reduced-motion: reduce) {
  .headline, .sub, .meter-band, .form-wrap { animation: none; }
  .stream { animation: none; }
}
</style>
