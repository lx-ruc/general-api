import { describe, it, expect } from 'vitest'
import { parseCurl } from './curl'

// 用户真实场景形状：火山方舟控制台复制的多行 curl（\ 续行 + 双引号 -H + 单引号多行 JSON -d）
// （密钥为假占位，真实 key 严禁入库——曾触发 GitHub Push Protection 拦截）
const ARK_EXAMPLE = `curl https://ark.cn-beijing.volces.com/api/v3/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ark-example-key-0001" \\
  -d '{
    "model": "glm-5-2-260617",
    "messages": [
      {"role": "system","content": "你是人工智能助手."},
      {"role": "user","content": "你好"}
    ]
  }'`

describe('parseCurl：完整 curl 示例', () => {
  it('解析火山方舟多行示例（续行/双引号 header/单引号多行 JSON）', () => {
    const r = parseCurl(ARK_EXAMPLE)
    expect(r).not.toBeNull()
    expect(r!.baseUrl).toBe('https://ark.cn-beijing.volces.com/api/v3')
    expect(r!.path).toBe('/chat/completions')
    expect(r!.apiKey).toBe('ark-example-key-0001')
    expect(r!.model).toBe('glm-5-2-260617')
    expect(r!.vendor).toBe('volces')
    expect(r!.host).toBe('ark.cn-beijing.volces.com')
  })

  it('单引号 header + --data-raw + 单行无续行', () => {
    const r = parseCurl(
      `curl -X POST 'https://api.deepseek.com/v1/chat/completions' ` +
        `-H 'Authorization: Bearer sk-abc123' --data-raw '{"model":"deepseek-chat","messages":[]}'`,
    )
    expect(r!.baseUrl).toBe('https://api.deepseek.com/v1')
    expect(r!.path).toBe('/chat/completions')
    expect(r!.apiKey).toBe('sk-abc123')
    expect(r!.model).toBe('deepseek-chat')
    expect(r!.vendor).toBe('deepseek')
  })

  it('URL 带 query 参数时剥离 query', () => {
    const r = parseCurl(
      `curl 'https://api.example.com/v1/chat/completions?foo=bar' -H 'Authorization: Bearer k1' -d '{"model":"m1"}'`,
    )
    expect(r!.baseUrl).toBe('https://api.example.com/v1')
    expect(r!.path).toBe('/chat/completions')
  })

  it('embeddings 端点', () => {
    const r = parseCurl(
      `curl https://host.example.com/v1/embeddings -H 'Authorization: Bearer k' -d '{"model":"e1","input":"x"}'`,
    )
    expect(r!.path).toBe('/embeddings')
    expect(r!.model).toBe('e1')
  })

  it('x-api-key 头兜底（无 Authorization 时）', () => {
    const r = parseCurl(
      `curl https://api.example.com/v1/chat/completions -H 'x-api-key: kk' -d '{"model":"m"}'`,
    )
    expect(r!.apiKey).toBe('kk')
  })

  it('Authorization 无 Bearer 前缀时取整个值', () => {
    const r = parseCurl(
      `curl https://api.example.com/v1/chat/completions -H 'Authorization: rawtoken' -d '{"model":"m"}'`,
    )
    expect(r!.apiKey).toBe('rawtoken')
  })

  it('双引号 -d 内转义引号可解析出 model', () => {
    const r = parseCurl(
      `curl https://api.example.com/v1/chat/completions -H "Authorization: Bearer k" -d "{\\"model\\":\\"m2\\"}"`,
    )
    expect(r!.model).toBe('m2')
  })
})

describe('parseCurl：裸 URL 与降级', () => {
  it('只贴带端点后缀的 URL：拆出 base/path，提示缺密钥', () => {
    const r = parseCurl('https://open.bigmodel.cn/api/paas/v4/chat/completions')
    expect(r!.baseUrl).toBe('https://open.bigmodel.cn/api/paas/v4')
    expect(r!.path).toBe('/chat/completions')
    expect(r!.apiKey).toBeUndefined()
    expect(r!.model).toBeUndefined()
    expect(r!.vendor).toBe('zhipu')
    expect(r!.warnings.length).toBeGreaterThan(0)
  })

  it('只贴以版本段结尾的裸 Base URL（火山 /api/v3）：path 取 /chat/completions', () => {
    const r = parseCurl('https://ark.cn-beijing.volces.com/api/v3')
    expect(r!.baseUrl).toBe('https://ark.cn-beijing.volces.com/api/v3')
    expect(r!.path).toBe('/chat/completions')
  })

  it('只贴无版本段的裸域名（DeepSeek）：path 取 /v1/chat/completions', () => {
    const r = parseCurl('https://api.deepseek.com')
    expect(r!.baseUrl).toBe('https://api.deepseek.com')
    expect(r!.path).toBe('/v1/chat/completions')
  })

  it('裸 URL 以 /v1 结尾（Moonshot 风格）：path 取 /chat/completions 而非再叠一层 /v1', () => {
    const r = parseCurl('https://api.moonshot.cn/v1')
    expect(r!.baseUrl).toBe('https://api.moonshot.cn/v1')
    expect(r!.path).toBe('/chat/completions')
  })

  it('body 不是合法 JSON：model 缺失 + 警告，其余字段仍解析', () => {
    const r = parseCurl(
      `curl https://api.example.com/v1/chat/completions -H 'Authorization: Bearer k' -d 'not json'`,
    )
    expect(r!.baseUrl).toBe('https://api.example.com/v1')
    expect(r!.apiKey).toBe('k')
    expect(r!.model).toBeUndefined()
    expect(r!.warnings.some((w) => w.includes('JSON'))).toBe(true)
  })

  it('合法 JSON 但无 model 字段：警告', () => {
    const r = parseCurl(
      `curl https://api.example.com/v1/chat/completions -H 'Authorization: Bearer k' -d '{"messages":[]}'`,
    )
    expect(r!.model).toBeUndefined()
    expect(r!.warnings.some((w) => w.includes('model'))).toBe(true)
  })

  it('未知域名：vendor 为空串', () => {
    const r = parseCurl('https://api.foo.com/v1/chat/completions')
    expect(r!.vendor).toBe('')
  })
})

describe('parseCurl：端点方言归一化', () => {
  // 用户真实踩坑：火山控制台给的 curl 打的是 /api/v3/responses（Responses API，input 数组请求体），
  // 网关只会说 Chat Completions 方言，须归一化到同目录 /chat/completions，否则拼出
  // /api/v3/responses/v1/chat/completions 之类的 404 路径
  it('火山方舟 /responses 端点：base 剥掉 /responses，path 归一化为 /chat/completions 并警告', () => {
    const r = parseCurl(
      `curl --location 'https://ark.cn-beijing.volces.com/api/v3/responses' ` +
        `--header "Authorization: Bearer ark-test-key-000" ` +
        `--header 'Content-Type: application/json' ` +
        `--data '{"model": "deepseek-v4-pro-260425", "stream": true, "input": [{"role": "user", "content": [{"type": "input_text", "text": "hi"}]}]}'`,
    )
    expect(r!.baseUrl).toBe('https://ark.cn-beijing.volces.com/api/v3')
    expect(r!.path).toBe('/chat/completions')
    expect(r!.model).toBe('deepseek-v4-pro-260425')
    expect(r!.apiKey).toBe('ark-test-key-000')
    expect(r!.warnings.some((w) => w.includes('/responses'))).toBe(true)
  })

  it('OpenAI 风格 /v1/responses 端点：归一化为 /v1 + /chat/completions', () => {
    const r = parseCurl(
      `curl https://api.openai.com/v1/responses -H 'Authorization: Bearer sk-t' -d '{"model":"gpt-x","input":"hi"}'`,
    )
    expect(r!.baseUrl).toBe('https://api.openai.com/v1')
    expect(r!.path).toBe('/chat/completions')
    expect(r!.warnings.some((w) => w.includes('Chat Completions'))).toBe(true)
  })

  it('旧版 /completions 端点（prompt 方言）：同样归一化为 /chat/completions', () => {
    const r = parseCurl(
      `curl https://api.example.com/v1/completions -H 'Authorization: Bearer k' -d '{"model":"m","prompt":"x"}'`,
    )
    expect(r!.baseUrl).toBe('https://api.example.com/v1')
    expect(r!.path).toBe('/chat/completions')
  })

  it('/chat/completions 优先于 /completions 匹配，完整端点不被误归一化', () => {
    const r = parseCurl('https://api.example.com/v1/chat/completions')
    expect(r!.path).toBe('/chat/completions')
    expect(r!.warnings.some((w) => w.includes('归一化'))).toBe(false)
  })

  it('/models 列模型端点：忽略后缀按裸 URL 启发式处理', () => {
    const r = parseCurl('https://ark.cn-beijing.volces.com/api/v3/models')
    expect(r!.baseUrl).toBe('https://ark.cn-beijing.volces.com/api/v3')
    expect(r!.path).toBe('/chat/completions')
    expect(r!.warnings.some((w) => w.includes('/models'))).toBe(true)
  })
})

describe('parseCurl：非法输入', () => {
  it('普通文字返回 null', () => {
    expect(parseCurl('你好，这是一段普通文字')).toBeNull()
  })

  it('只有 curl 命令名没有 URL 返回 null', () => {
    expect(parseCurl('curl -X POST -H "a: b"')).toBeNull()
  })

  it('空串返回 null', () => {
    expect(parseCurl('')).toBeNull()
    expect(parseCurl('   \n  ')).toBeNull()
  })
})
