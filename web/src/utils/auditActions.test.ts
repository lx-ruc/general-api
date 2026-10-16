import { describe, it, expect } from 'vitest'
import { actionName } from './auditActions'

describe('actionName：平台层操作 → 中文动作名', () => {
  it('精确命中（无参路径）', () => {
    expect(actionName('POST', '/api/platform/orgs')).toBe('新建客户')
    expect(actionName('PUT', '/api/platform/bank-info')).toBe('修改收款信息')
  })

  it(':占位段匹配任意单段（数字/字符串均可）', () => {
    expect(actionName('PUT', '/api/platform/channels/12/status')).toBe('启停渠道')
    expect(actionName('POST', '/api/platform/orgs/5/quota')).toBe('追加客户额度')
    expect(actionName('DELETE', '/api/platform/model-prices/deepseek-chat/3')).toBe('删除客户级定价')
    expect(actionName('POST', '/api/platform/admins/2/reset-password')).toBe('重置子管理员密码')
  })

  it('方法不同不串名', () => {
    expect(actionName('GET', '/api/platform/orgs')).toBe('GET /api/platform/orgs')
    expect(actionName('PUT', '/api/platform/orgs')).not.toBe('新建客户')
  })

  it('段数不同不误匹配（前缀相同）', () => {
    expect(actionName('DELETE', '/api/platform/channels/1/keys/2')).toBe('删除渠道 Key')
    expect(actionName('PUT', '/api/platform/channels/1/keys/2/status')).toBe('启停渠道 Key')
  })

  it('未命中回退 METHOD path 原样（org 层 / me 层写操作）', () => {
    expect(actionName('PUT', '/api/org/members/9')).toBe('PUT /api/org/members/9')
    expect(actionName('PUT', '/api/me/email')).toBe('PUT /api/me/email')
  })
})
