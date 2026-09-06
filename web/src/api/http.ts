import axios from 'axios'
import { ElMessage } from 'element-plus'

export interface ApiError {
  message: string
  type?: string
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
      localStorage.removeItem('tg_token')
      if (!location.hash.includes('/login')) {
        location.hash = '#/login'
        ElMessage.error('登录已过期，请重新登录')
      }
    } else {
      ElMessage.error(msg)
    }
    return Promise.reject(err)
  },
)

export default http
