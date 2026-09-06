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
