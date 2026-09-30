import axios from 'axios'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../stores/auth'

export interface ApiError {
  message: string
  type?: string
}

// 自定义请求配置：skipErrorToast = 调用方自己展示错误（如表单内联红字），拦截器不再弹全局 toast
declare module 'axios' {
  export interface AxiosRequestConfig {
    skipErrorToast?: boolean
  }
}

const http = axios.create({ timeout: 30000 })

http.interceptors.request.use((cfg) => {
  const t = localStorage.getItem('tg_token')
  if (t) cfg.headers.Authorization = `Bearer ${t}`
  return cfg
})

http.interceptors.response.use(
  (r) => r.data,
  (err) => {
    const msg = (err.response?.data as ApiError)?.error?.message || err.message || '请求失败'
    if (err.response?.status === 401) {
      // 401 = JWT 过期/失效：必须同时清 localStorage 与 Pinia store。
      // 路由守卫读的是 store 里的 token（内存），只清 localStorage 会被守卫
      // 判定「已登录访问 /login」弹回角色首页——过期用户困在 401 白屏页直到 F5
      try {
        useAuthStore().logout()
      } catch {
        localStorage.removeItem('tg_token') // store 尚未初始化（如登录页）时的兜底
      }
      if (!location.hash.includes('/login')) {
        location.hash = '#/login'
        ElMessage.error('登录已过期，请重新登录')
      }
    } else if (!err.config?.skipErrorToast) {
      ElMessage.error(msg)
    }
    // 拒绝时带后端文案：调用方 catch 里 e.message 是友好信息，
    // 而不是 axios 原始的 "Request failed with status code 400"
    return Promise.reject(new Error(msg))
  },
)

export default http
