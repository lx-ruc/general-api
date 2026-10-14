import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiLogin, apiMe, type UserInfo } from '../api/auth'

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string>(localStorage.getItem('tg_token') || '')
  const user = ref<UserInfo | null>(null)
  // 点/元汇率（1 元 = ppy 点，默认 100 万）：/api/me 下发，全站额度折元的唯一口径
  const ppy = ref<number>(1_000_000)

  async function signIn(username: string, password: string) {
    const resp = await apiLogin(username, password)
    token.value = resp.token
    user.value = resp.user
    if (resp.user?.points_per_yuan) ppy.value = resp.user.points_per_yuan
    localStorage.setItem('tg_token', resp.token)
  }

  async function fetchMe() {
    user.value = await apiMe()
    if (user.value?.points_per_yuan) ppy.value = user.value.points_per_yuan
    return user.value
  }

  function logout() {
    token.value = ''
    user.value = null
    localStorage.removeItem('tg_token')
  }

  return { token, user, ppy, signIn, fetchMe, logout }
})

export function homeOf(role?: string): string {
  switch (role) {
    case 'platform_admin':
      return '/platform/dashboard'
    case 'org_admin':
      return '/org/dashboard'
    case 'member':
      return '/member/models'
    default:
      return '/login'
  }
}
