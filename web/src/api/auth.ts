import http from './http'

export interface UserInfo {
  id: number
  username: string
  display_name: string
  role: 'platform_admin' | 'org_admin' | 'member'
  org_id: number | null
  org_name?: string
  status: number
  quota_limit: number | null
  quota_used: number
  last_login_at: number | null
  points_per_yuan?: number
}

export interface LoginResp {
  token: string
  expires_in: number
  user: UserInfo
}

export const apiLogin = (username: string, password: string) =>
  http.post<any, LoginResp>('/api/auth/login', { username, password })

export const apiMe = () => http.get<any, UserInfo>('/api/me')

export const apiChangePassword = (old_password: string, new_password: string) =>
  http.put<any, { message: string }>('/api/me/password', { old_password, new_password })

// ---- 忘记密码（邮箱验证 + 邮件链接重置）----
// skipErrorToast：错误文案由弹窗/页面内联展示，拦截器不重复弹全局 toast

export const apiForgotPassword = (email: string) =>
  http.post<any, { message: string; dev_link?: string }>(
    '/api/auth/forgot-password', { email }, { skipErrorToast: true })

export const apiResetPassword = (token: string, new_password: string) =>
  http.post<any, { message: string }>(
    '/api/auth/reset-password', { token, new_password }, { skipErrorToast: true })

// ---- 管理面访问令牌（仅平台/客户管理员；程序化对接管理 API 用）----

export interface AccessToken {
  id: number
  name: string
  prefix: string
  status: number
  expires_at: number
  last_used_at: number
  created_at: number
}

export interface AccessTokenCreated {
  token: string
  id: number
  prefix: string
  expires_at: number
}

export const apiListAccessTokens = (): Promise<AccessToken[]> =>
  http.get<any, AccessToken[]>('/api/me/tokens')

export const apiCreateAccessToken = (name: string, expires_days: number): Promise<AccessTokenCreated> =>
  http.post<any, AccessTokenCreated>('/api/me/tokens', { name, expires_days })

export const apiRevokeAccessToken = (id: number): Promise<{ message: string }> =>
  http.delete<any, { message: string }>(`/api/me/tokens/${id}`)
