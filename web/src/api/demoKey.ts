import http from './http'

// 对外演示体验密钥（管理台配置；GET 即幂等开通专用体验账号）。
// 明文可反复查看是有意设计：演示密钥本就要反复发给来访客户，
// 与普通密钥「只显示一次」的约定不同。
export interface DemoKeyConfig {
  provisioned: boolean
  key: string
  key_prefix: string
  key_id: number
  enabled: boolean
  expires_at: number // unix 秒；0=永久
  user: { id: number; username: string; quota_limit: number | null; quota_used: number }
  org: { id: number; name: string; quota_limit: number; quota_used: number }
  models: string[]
}

export interface DemoKeyUpdate {
  enabled?: boolean
  expires_at?: number // unix 秒；0=永久
  quota_points?: number // 体验总额度（点）目标值
  models?: string[] // 授权模型全量替换；缺省=不修改
}

export const apiGetDemoKey = () => http.get<any, DemoKeyConfig>('/api/platform/demo-key')
export const apiConfigureDemoKey = (data: DemoKeyUpdate) =>
  http.put<any, DemoKeyConfig>('/api/platform/demo-key', data)
export const apiRotateDemoKey = () =>
  http.post<any, { key: string; message: string }>('/api/platform/demo-key/rotate')
