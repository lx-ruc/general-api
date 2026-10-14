import http from './http'

// 对外演示体验密钥（管理台配置；GET 即幂等开通专用体验账号）。
// 界面只展示打码串，取完整明文统一走 /demo-key/copy（进剪贴板不回显）；
// 轮换响应里的一次性明文除外。
export interface DemoKeyConfig {
  provisioned: boolean
  key_masked: string
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
export const apiCopyDemoKey = (): Promise<{ key: string }> =>
  http.post<any, { key: string }>('/api/platform/demo-key/copy')
