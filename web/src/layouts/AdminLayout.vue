<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessageBox } from 'element-plus'
import { useAuthStore } from '../stores/auth'
import { roleNames } from '../utils/format'
import { apiChangePassword } from '../api/auth'

const auth = useAuthStore()
const router = useRouter()
const route = useRoute()

interface MenuItem {
  index: string
  title: string
  icon: string
}

const menus = computed<MenuItem[]>(() => {
  switch (auth.user?.role) {
    case 'platform_admin':
      return [
        { index: '/platform/dashboard', title: '数据看板', icon: 'Odometer' },
        { index: '/platform/orgs', title: '公司管理', icon: 'OfficeBuilding' },
        { index: '/platform/channels', title: '渠道管理', icon: 'Connection' },
        { index: '/platform/models', title: '模型定价', icon: 'PriceTag' },
        { index: '/platform/usage', title: '调用日志', icon: 'Document' },
      ]
    case 'org_admin':
      return [
        { index: '/org/dashboard', title: '公司看板', icon: 'Odometer' },
        { index: '/org/members', title: '员工管理', icon: 'User' },
        { index: '/org/requests', title: '额度申请', icon: 'Bell' },
        { index: '/org/keys', title: '密钥一览', icon: 'Key' },
        { index: '/org/usage', title: '调用日志', icon: 'Document' },
      ]
    default:
      return [
        { index: '/member/models', title: '可用模型', icon: 'Goods' },
        { index: '/member/keys', title: '我的密钥', icon: 'Key' },
        { index: '/member/usage', title: '我的用量', icon: 'TrendCharts' },
        { index: '/member/quota', title: '额度申请', icon: 'Bell' },
        { index: '/member/docs', title: '接入文档', icon: 'Notebook' },
      ]
  }
})

const activeMenu = computed(() => route.path)

function handleLogout() {
  auth.logout()
  router.push('/login')
}

// 修改密码对话框
const pwdVisible = ref(false)
const pwdForm = ref({ old_password: '', new_password: '', confirm: '' })
async function submitPassword() {
  if (!pwdForm.value.old_password || !pwdForm.value.new_password) {
    return
  }
  if (pwdForm.value.new_password !== pwdForm.value.confirm) {
    ElMessageBox.alert('两次输入的新密码不一致', '提示')
    return
  }
  await apiChangePassword(pwdForm.value.old_password, pwdForm.value.new_password)
  pwdVisible.value = false
  pwdForm.value = { old_password: '', new_password: '', confirm: '' }
  ElMessageBox.alert('密码已修改', '成功')
}
</script>

<template>
  <el-container class="layout">
    <el-aside width="220px" class="aside">
      <div class="logo">
        <el-icon :size="22"><Lightning /></el-icon>
        <span>token 中转站</span>
      </div>
      <el-menu :default-active="activeMenu" router background-color="#001529" text-color="#a6adb4"
        active-text-color="#ffffff" class="menu">
        <el-menu-item v-for="m in menus" :key="m.index" :index="m.index">
          <el-icon><component :is="m.icon" /></el-icon>
          <span>{{ m.title }}</span>
        </el-menu-item>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="header">
        <div class="header-left">
          <span v-if="auth.user?.org_name" class="org-name">{{ auth.user.org_name }}</span>
        </div>
        <div class="header-right">
          <el-dropdown>
            <span class="user-info">
              <el-icon><UserFilled /></el-icon>
              {{ auth.user?.display_name || auth.user?.username }}
              <el-tag size="small" type="info">{{ roleNames[auth.user?.role || ''] || auth.user?.role }}</el-tag>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item @click="pwdVisible = true">修改密码</el-dropdown-item>
                <el-dropdown-item divided @click="handleLogout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>
      <el-main class="main">
        <router-view />
      </el-main>
    </el-container>
  </el-container>

  <el-dialog v-model="pwdVisible" title="修改密码" width="420px">
    <el-form label-width="90px">
      <el-form-item label="原密码">
        <el-input v-model="pwdForm.old_password" type="password" show-password />
      </el-form-item>
      <el-form-item label="新密码">
        <el-input v-model="pwdForm.new_password" type="password" show-password placeholder="至少 6 位" />
      </el-form-item>
      <el-form-item label="确认新密码">
        <el-input v-model="pwdForm.confirm" type="password" show-password />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="pwdVisible = false">取消</el-button>
      <el-button type="primary" @click="submitPassword">确定</el-button>
    </template>
  </el-dialog>
</template>

<style scoped>
.layout { height: 100vh; }
.aside { background: #001529; display: flex; flex-direction: column; }
.logo {
  height: 56px; display: flex; align-items: center; justify-content: center;
  gap: 8px; color: #fff; font-size: 16px; font-weight: 600;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}
.menu { border-right: none; flex: 1; }
.header {
  background: #fff; display: flex; align-items: center; justify-content: space-between;
  border-bottom: 1px solid #e4e7ed; height: 56px;
}
.org-name { font-weight: 600; color: #303133; }
.user-info { display: flex; align-items: center; gap: 6px; cursor: pointer; outline: none; }
.main { background: #f5f7fa; overflow-y: auto; }
</style>
