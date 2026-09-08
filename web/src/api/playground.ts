import http from './http'

// 在线体验：模型列表 + 流式对话（/api/playground/chat，OpenAI 兼容协议原样透传）

export interface PGModel {
  id: number
  name: string
  display_name: string
  vendor: string
}

export interface PGUsage {
  prompt_tokens: number
  completion_tokens: number
}

export async function apiPlaygroundModels(): Promise<PGModel[]> {
  const r = await http.get('/api/playground/models') as { models: PGModel[] }
  return r.models || []
}

export interface PGChatHandlers {
  onDelta: (text: string) => void
  onUsage?: (usage: PGUsage) => void
  signal?: AbortSignal
}

// playgroundChat 发起流式对话：SSE 逐块解析 delta 与末块 usage；
// 非流式响应（错误或缓存命中）整体解析后一次性回调
export async function playgroundChat(
  body: { model: string; messages: { role: string; content: string }[] },
  handlers: PGChatHandlers,
): Promise<void> {
  const token = localStorage.getItem('tg_token')
  const resp = await fetch('/api/playground/chat', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: JSON.stringify({ ...body, stream: true }),
    signal: handlers.signal,
  })

  if (!resp.ok) {
    let msg = `请求失败（HTTP ${resp.status}）`
    try {
      const data = await resp.json()
      msg = data?.error?.message || msg
    } catch { /* 保持默认消息 */ }
    throw new Error(msg)
  }

  const ctype = resp.headers.get('Content-Type') || ''
  if (!ctype.includes('text/event-stream')) {
    // 缓存命中等场景：整体 JSON，choices[0].message.content 一次性回调
    const data = await resp.json()
    const content = data?.choices?.[0]?.message?.content
    if (content) handlers.onDelta(content)
    if (data?.usage) handlers.onUsage?.(data.usage)
    return
  }

  const reader = resp.body!.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const lines = buf.split('\n')
    buf = lines.pop() || ''
    for (const line of lines) {
      const s = line.trim()
      if (!s.startsWith('data:')) continue
      const payload = s.slice(5).trim()
      if (!payload || payload === '[DONE]') continue
      try {
        const chunk = JSON.parse(payload)
        const delta = chunk?.choices?.[0]?.delta?.content
        if (delta) handlers.onDelta(delta)
        if (chunk?.usage) handlers.onUsage?.(chunk.usage)
      } catch { /* 忽略无法解析的块 */ }
    }
  }
}
