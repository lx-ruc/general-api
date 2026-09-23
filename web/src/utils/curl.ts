// curl 示例解析：管理员从厂商控制台复制 curl（或裸 URL）粘贴进渠道新建弹窗，
// 前端本地解析出 base_url / 端点 path / API 密钥 / 模型名，预填表单。
// 纯字符串解析，不发起任何请求；密钥只落入既有 upstream_key 字段（后端 AES-GCM 加密存储）。

/** Chat Completions 方言端点后缀：原样保留为渠道 path */
const ENDPOINT_SUFFIXES = ['/chat/completions', '/embeddings']
/** 非 Chat Completions 方言端点后缀：剥离后改用同目录 /chat/completions（网关不做方言转换） */
const DIALECT_SUFFIXES = ['/responses', '/completions']
/** 与推理无关的端点后缀：剥离后按裸 URL 启发式处理 */
const IGNORED_SUFFIXES = ['/models']

/** 按域名猜厂商标识（host 等于域名或为其子域名时命中） */
const VENDOR_DOMAINS: ReadonlyArray<readonly [string, string]> = [
  ['api.deepseek.com', 'deepseek'],
  ['open.bigmodel.cn', 'zhipu'],
  ['dashscope.aliyuncs.com', 'aliyun'],
  ['volces.com', 'volces'],
]

const HEADER_FLAGS = new Set(['-h', '--header'])
const BODY_FLAGS = new Set(['-d', '--data', '--data-raw', '--data-binary'])

export interface ParsedCurl {
  /** 剥掉端点后缀后的 base（如 https://ark.cn-beijing.volces.com/api/v3） */
  baseUrl: string
  /**
   * 渠道请求 path（保证 baseUrl + path 是 Chat Completions 端点）：
   * 已知方言端点原样保留；/responses、/completions 归一化为 /chat/completions；
   * 裸 URL 以 /vN 版本段结尾取 /chat/completions，否则取 /v1/chat/completions
   */
  path: string
  /** API 密钥（Authorization: Bearer / api-key / x-api-key） */
  apiKey?: string
  /** 请求体 JSON 的 model 字段 */
  model?: string
  /** URL 的 host，用于默认渠道名 */
  host: string
  /** 按域名猜测的厂商标识，未命中为空串 */
  vendor: string
  /** 非致命问题（缺密钥、body 非法 JSON 等），逐条提示给用户 */
  warnings: string[]
}

/**
 * shell 风格分词：处理 `\`+换行续行（丢弃）、单引号字面串、双引号串（`\x` 转义）、
 * 引号外空白分词与 `\x` 转义。返回去引号后的 token 数组。
 */
function tokenize(text: string): string[] {
  const tokens: string[] = []
  let cur = ''
  let started = false
  const push = (): void => {
    if (started) {
      tokens.push(cur)
      cur = ''
      started = false
    }
  }
  let i = 0
  while (i < text.length) {
    const ch = text[i]
    // 续行：反斜杠 + 换行（兼容 \r\n 与 \n）
    if (ch === '\\' && (text[i + 1] === '\n' || text[i + 1] === '\r')) {
      i += text[i + 1] === '\r' && text[i + 2] === '\n' ? 3 : 2
      continue
    }
    if (ch === "'") {
      // 单引号：到下一个 ' 为止，内容原样（含换行）
      started = true
      i++
      while (i < text.length && text[i] !== "'") cur += text[i++]
      i++
      continue
    }
    if (ch === '"') {
      // 双引号：反斜杠转义下一字符
      started = true
      i++
      while (i < text.length && text[i] !== '"') {
        if (text[i] === '\\' && i + 1 < text.length) {
          cur += text[i + 1]
          i += 2
        } else {
          cur += text[i++]
        }
      }
      i++
      continue
    }
    if (ch === ' ' || ch === '\t' || ch === '\n' || ch === '\r') {
      push()
      i++
      continue
    }
    if (ch === '\\' && i + 1 < text.length) {
      cur += text[i + 1]
      started = true
      i += 2
      continue
    }
    cur += ch
    started = true
    i++
  }
  push()
  return tokens
}

/** 从 token 里找第一个 http(s):// 开头的 URL，剥掉 query/hash 与尾部斜杠；找不到返回 null */
function extractUrl(tokens: readonly string[]): string | null {
  for (const t of tokens) {
    const lower = t.toLowerCase()
    if (lower.startsWith('http://') || lower.startsWith('https://')) {
      const cleaned = t.split(/[?#]/)[0].replace(/\/+$/, '')
      return cleaned === '' ? null : cleaned
    }
  }
  return null
}

/** 从 URL 提取 host（协议后到第一个 / : 之前） */
function extractHost(url: string): string {
  const afterProto = url.replace(/^https?:\/\//i, '')
  const m = afterProto.match(/^[^/:]+/)
  return (m ? m[0] : '').toLowerCase()
}

/** URL 的路径部分是否以 /v3 这类版本段结尾（如 …/api/v3、…/compatible-mode/v1） */
function endsWithVersionSegment(url: string): boolean {
  const path = url.replace(/^https?:\/\/[^/]+/i, '')
  return /\/v\d+(?:\.\d+)?$/i.test(path)
}

/** host 等于域名或为其子域名时返回对应厂商标识，否则空串 */
function guessVendor(host: string): string {
  for (const [domain, vendor] of VENDOR_DOMAINS) {
    if (host === domain || host.endsWith('.' + domain)) return vendor
  }
  return ''
}

/** 从 -H/--header token 中提取密钥：Authorization: Bearer 优先，api-key / x-api-key 兜底 */
function extractApiKey(headers: readonly string[]): string | undefined {
  let apiKeyValue: string | undefined
  for (const h of headers) {
    const idx = h.indexOf(':')
    if (idx <= 0) continue
    const name = h.slice(0, idx).trim().toLowerCase()
    const value = h.slice(idx + 1).trim()
    if (name === 'authorization' && value !== '') {
      // "Bearer xxx" 取空格后部分；无前缀（裸 token）取整个值
      const sp = value.indexOf(' ')
      return sp > 0 ? value.slice(sp + 1).trim() : value
    }
    if ((name === 'api-key' || name === 'x-api-key') && value !== '') {
      apiKeyValue = value
    }
  }
  return apiKeyValue
}

/** 从 body 字符串提取 model 字段；解析失败或缺失返回 undefined 并给出原因 */
function extractModel(
  body: string | undefined,
  warnings: string[],
): string | undefined {
  if (body === undefined) return undefined
  try {
    const parsed: unknown = JSON.parse(body)
    if (parsed !== null && typeof parsed === 'object' && 'model' in parsed) {
      const model = (parsed as { model?: unknown }).model
      if (typeof model === 'string' && model !== '') return model
    }
    warnings.push('请求体中没有 model 字段，请手动填写模型名')
  } catch {
    warnings.push('请求体不是合法 JSON，未能提取模型名，请手动填写')
  }
  return undefined
}

/**
 * 解析粘贴的 curl 命令或裸 URL。
 * 返回 null 表示完全没有识别出 URL（粘贴内容不是预期的格式）；
 * 其余情况尽量提取可用字段，问题写入 warnings 由调用方逐条提示。
 */
export function parseCurl(text: string): ParsedCurl | null {
  const tokens = tokenize(text)
  const url = extractUrl(tokens)
  if (url === null) return null

  const warnings: string[] = []

  // 拆 base + path：保证 baseUrl + path 拼出的上游 URL 是 Chat Completions 端点
  let baseUrl = url
  let path = ''
  for (const suffix of ENDPOINT_SUFFIXES) {
    if (url.endsWith(suffix)) {
      baseUrl = url.slice(0, url.length - suffix.length)
      path = suffix
      break
    }
  }
  if (path === '') {
    // 非 Chat Completions 方言端点：归一化到同目录 /chat/completions
    // （火山控制台的 curl 常打 /api/v3/responses，网关只会中转 Chat Completions 方言）
    for (const suffix of DIALECT_SUFFIXES) {
      if (url.endsWith(suffix)) {
        baseUrl = url.slice(0, url.length - suffix.length)
        path = '/chat/completions'
        warnings.push(
          `端点 ${suffix} 不是 Chat Completions 方言，已改用同目录的 /chat/completions（网关仅支持 Chat Completions 方言中转）`,
        )
        break
      }
    }
  }
  if (path === '') {
    // 裸 Base URL（或剥掉 /models 后）：以 /vN 版本段结尾时补 /chat/completions，
    // 否则补 /v1/chat/completions —— 避免在 /api/v3 之后再叠出 /api/v3/v1/… 的 404 路径
    let stripped = url
    for (const suffix of IGNORED_SUFFIXES) {
      if (stripped.endsWith(suffix)) {
        stripped = stripped.slice(0, stripped.length - suffix.length)
        warnings.push(`已忽略 ${suffix} 端点后缀，按 Base URL 识别`)
      }
    }
    baseUrl = stripped
    path = endsWithVersionSegment(stripped) ? '/chat/completions' : '/v1/chat/completions'
  }

  // 收集 -H 的 header 值与 -d 的 body
  const headers: string[] = []
  let body: string | undefined
  for (let i = 0; i < tokens.length; i++) {
    const flag = tokens[i].toLowerCase()
    if (HEADER_FLAGS.has(flag) && i + 1 < tokens.length) {
      headers.push(tokens[i + 1])
      i++
    } else if (BODY_FLAGS.has(flag) && i + 1 < tokens.length) {
      body = tokens[i + 1]
      i++
    }
  }

  const apiKey = extractApiKey(headers)
  if (apiKey === undefined) {
    warnings.push('未识别到 API 密钥（Authorization / api-key / x-api-key），请手动填写')
  }
  const model = extractModel(body, warnings)
  const host = extractHost(url)

  return { baseUrl, path, apiKey, model, host, vendor: guessVendor(host), warnings }
}
