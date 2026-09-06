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
const forgotVisible = ref(false)

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
          统一接入，按量计费
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

          <div class="form-links">
            <button type="button" class="link" @click="forgotVisible = true">忘记密码？</button>
            <span class="sep">·</span>
            <button type="button" class="link" @click="router.push('/register')">注册公司</button>
          </div>
        </form>
        <p class="form-foot mono">POST /api/auth/login → JWT · 有效期 12h</p>
      </section>

      <!-- 忘记密码：分层找回指引 -->
      <el-dialog v-model="forgotVisible" title="忘记密码了？" width="440px">
        <p class="forgot-lead">按你的账号类型找对应的管理员重置：</p>
        <ul class="forgot-list">
          <li>
            <b>员工账号</b> — 联系本公司管理员：
            公司管理员在「员工管理 → 更多 → 重置密码」为你重置。
          </li>
          <li>
            <b>公司管理员账号</b> — 联系平台管理员：
            在「公司管理 → 公司详情 → 重置管理员密码」重置。
          </li>
          <li>
            <b>平台管理员账号</b> — 服务器上执行运维命令重置：
            <code>./token-gateway -reset-password admin:新密码</code>
          </li>
        </ul>
        <p class="forgot-note">重置后请尽快登录，在右上角「修改密码」改成自己的密码。</p>
      </el-dialog>
    </main>
  </div>
</template>

<style scoped>
.login {
  min-height: 100vh;
  background:
    radial-gradient(1100px 520px at 10% -12%, rgba(18, 164, 98, 0.10), transparent 62%),
    var(--tg-paper);
  display: flex; align-items: center; justify-content: center;
  padding: 32px 20px;
  color: var(--tg-ink);
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
.brand-row { display: flex; align-items: center; gap: 10px; margin-bottom: 20px; }
.brand-dot {
  width: 10px; height: 10px; border-radius: 50%;
  background: var(--tg-green);
  box-shadow: 0 0 0 4px rgba(18, 164, 98, 0.18);
}
.brand-name { font-size: 14px; font-weight: 600; letter-spacing: 0.04em; color: var(--tg-graphite); }

.headline {
  font-size: clamp(30px, 4.2vw, 44px);
  line-height: 1.22; font-weight: 700;
  margin: 0 0 16px; letter-spacing: 0.01em; color: var(--tg-ink);
  animation: rise 0.6s cubic-bezier(0.2, 0.8, 0.3, 1) both;
}
.sub {
  font-size: 14px; line-height: 1.9; color: var(--tg-graphite);
  max-width: 34em; margin: 0 0 32px;
  animation: rise 0.6s 0.08s cubic-bezier(0.2, 0.8, 0.3, 1) both;
}
@keyframes rise {
  from { opacity: 0; transform: translateY(10px); }
  to { opacity: 1; transform: none; }
}

/* ---------- 计量条（亮色：白卡 + 绿色 code 芯片） ---------- */
.meter-band {
  margin: 0;
  border: 1px solid var(--tg-line);
  border-radius: 10px;
  background: var(--tg-surface);
  overflow: hidden;
  animation: rise 0.6s 0.16s cubic-bezier(0.2, 0.8, 0.3, 1) both;
}
.stream-mask {
  overflow: hidden; border-bottom: 1px solid var(--tg-line); padding: 12px 0;
  -webkit-mask-image: linear-gradient(90deg, transparent, #000 8%, #000 92%, transparent);
  mask-image: linear-gradient(90deg, transparent, #000 8%, #000 92%, transparent);
}
.stream {
  display: flex; gap: 10px; white-space: nowrap; width: max-content;
  animation: flow 26s linear infinite;
  padding-left: 22px;
}
@keyframes flow { to { transform: translateX(-50%); } }
.stream-chip {
  font-family: ui-monospace, 'SF Mono', Menlo, monospace;
  font-size: 12px; color: var(--tg-green-ink);
  background: var(--tg-green-wash);
  border-radius: 999px;
  padding: 2px 10px;
}
.stream-caret { color: var(--tg-green); margin-left: 7px; }

.meter-read {
  display: flex; align-items: baseline; gap: 10px;
  padding: 12px 16px;
}
.relayed { font-size: 22px; font-weight: 600; color: var(--tg-ink); font-variant-numeric: tabular-nums; }
.meter-unit { font-size: 12px; color: var(--tg-muted); }
.meter-rate { font-size: 12px; color: var(--tg-green-ink); }
.meter-ticks { margin-left: auto; display: flex; gap: 3px; align-self: center; }
.meter-ticks i {
  width: 2px; height: 10px; border-radius: 1px;
  background: #a9c9b8;
}
.meter-ticks i:nth-child(4n + 1) { height: 14px; background: var(--tg-green); }

/* ---------- 登录表单 ---------- */
.form-wrap { animation: rise 0.6s 0.22s cubic-bezier(0.2, 0.8, 0.3, 1) both; }
.form {
  background: var(--tg-surface);
  border: 1px solid var(--tg-line);
  border-radius: 12px;
  padding: 30px 28px 26px;
  box-shadow: 0 12px 32px rgba(27, 43, 36, 0.07);
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

/* 亮绿底 + 墨字（4.70:1），hover 更亮 */
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

.form-foot {
  margin: 9px 0 0; text-align: center;
  font-size: 11px; color: var(--tg-graphite);
}

.form-links { margin-top: 12px; text-align: center; }
.sep { color: var(--tg-line-strong); margin: 0 6px; font-size: 12px; }
.link {
  background: none; border: none; cursor: pointer;
  font-size: 12.5px; color: var(--tg-green-ink);
}
.link:hover { text-decoration: underline; }

.forgot-lead { margin: 0 0 10px; font-size: 13.5px; color: var(--tg-ink); }
.forgot-list { margin: 0; padding-left: 18px; font-size: 13px; color: var(--tg-graphite); line-height: 2; }
.forgot-list b { color: var(--tg-ink); }
.forgot-list code {
  background: var(--tg-green-wash); color: var(--tg-green-ink);
  border-radius: 4px; padding: 1px 6px; font-size: 12px;
}
.forgot-note { margin: 12px 0 0; font-size: 12.5px; color: var(--tg-muted); }

@media (prefers-reduced-motion: reduce) {
  .headline, .sub, .meter-band, .form-wrap { animation: none; }
  .stream { animation: none; }
}
</style>
