import { defineStore } from 'pinia'
import { ref } from 'vue'
import { apiLogin, apiMe, type UserInfo } from '../api/auth'

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string>(localStorage.getItem('tg_token') || '')
  const user = ref<UserInfo | null>(null)

  async function signIn(username: string, password: string) {
    const resp = await apiLogin(username, password)
    token.value = resp.token
    user.value = resp.user
    localStorage.setItem('tg_token', resp.token)
  }

  async function fetchMe() {
    user.value = await apiMe()
    return user.value
  }

  function logout() {
    token.value = ''
    user.value = null
    localStorage.removeItem('tg_token')
  }

  return { token, user, signIn, fetchMe, logout }
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
