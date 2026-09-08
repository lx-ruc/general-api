<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore, homeOf } from '../stores/auth'
import SiteTopBar from '../components/SiteTopBar.vue'
import LoginShowcase from '../components/LoginShowcase.vue'

const router = useRouter()
const auth = useAuthStore()
const loading = ref(false)
const username = ref('')
const password = ref('')
const errorMsg = ref('')
const forgotVisible = ref(false)

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
    <SiteTopBar />
    <div class="login-body">
      <!-- 左：网关调度看板（品牌装置，纯展示） -->
      <LoginShowcase class="showcase" />

      <!-- 右：登录 -->
      <section class="form-wrap">
        <form class="form" @submit.prevent="submit">
          <h2 class="form-title">登录general API</h2>

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
            <button type="button" class="link" @click="router.push('/register')">注册</button>
          </div>
        </form>

        <!-- 忘记密码：分层找回指引 -->
        <el-dialog v-model="forgotVisible" title="忘记密码了？" width="440px">
          <p class="forgot-lead">按你的账号类型找对应的管理员重置：</p>
          <ul class="forgot-list">
            <li>
              <b>子账号账号</b> — 联系本客户管理员：
              客户管理员在「子账号管理 → 更多 → 重置密码」为你重置。
            </li>
            <li>
              <b>客户管理员账号</b> — 联系系统管理员：
              在「客户管理 → 客户详情 → 重置管理员密码」重置。
            </li>
            <li>
              <b>系统管理员账号</b> — 服务器上执行运维命令重置：
              <code>./token-gateway -reset-password admin:新密码</code>
            </li>
          </ul>
          <p class="forgot-note">重置后请尽快登录，在右上角「修改密码」改成自己的密码。</p>
        </el-dialog>
      </section>
    </div>
  </div>
</template>

<style scoped>
.login {
  height: 100vh;
  height: 100dvh;
  background: var(--tg-paper);
  display: flex; flex-direction: column;
  color: var(--tg-ink);
}

/* 整屏一分为二（无边框卡片）：左看板铺满剩余宽度，右登录白色竖栏 */
.login-body {
  flex: 1;
  min-height: 0;
  display: flex;
}
.showcase { flex: 1 1 auto; min-width: 0; }

/* 右栏：表单平铺在白色面板上（margin:auto 居中，矮屏时不裁切） */
.form-wrap {
  flex: none; width: 500px; min-width: 0; box-sizing: border-box;
  background: var(--tg-surface);
  border-left: 1px solid var(--tg-line);
  display: flex;
  padding: 48px 60px;
  overflow-y: auto;
  animation: rise 0.6s 0.22s cubic-bezier(0.2, 0.8, 0.3, 1) both;
}
@keyframes rise {
  from { opacity: 0; transform: translateY(10px); }
  to { opacity: 1; transform: none; }
}

/* 窄屏：看板退场，登录居中 */
@media (max-width: 980px) {
  .login { height: auto; min-height: 100dvh; }
  .login-body { justify-content: center; }
  .showcase { display: none; }
  .form-wrap { width: 100%; border-left: none; padding: 32px 24px; }
}

/* ---------- 登录表单（平铺，无卡片框） ---------- */
.form { width: 100%; max-width: 340px; margin: auto; }
.form-title { margin: 0 0 24px; font-size: 20px; font-weight: 600; color: var(--tg-ink); }

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
  .form-wrap { animation: none; }
}
</style>
