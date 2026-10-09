import { describe, it, expect, vi } from 'vitest'
import { marked } from 'marked'

// markdown.ts 顶层有 DOMPurify.addHook（浏览器环境），node 测试环境无 window，
// mock 掉 dompurify 使纯函数 fixCjkBoldEmphasis 可直接测
vi.mock('dompurify', () => ({
  default: { addHook: vi.fn(), sanitize: (html: string) => html },
}))

import { fixCjkBoldEmphasis } from './markdown'

// 中文场景 ** 粗体修复：marked 对「汉字后紧跟 ** 且 ** 后是标点」不认定为粗体
// 开界定符（CommonMark flanking 规则），星号原样露出。修复后插入空格恢复渲染，
// 代码块/行内代码/普通粗体不受影响。

// 端到端断言：修复后的文本经 marked 渲染必须出 <strong>（绕开 DOMPurify，node 环境无 DOM）
const rendersBold = (raw: string) =>
  marked.parse(fixCjkBoldEmphasis(raw), { async: false }).includes('<strong>')

describe('fixCjkBoldEmphasis', () => {
  it('中文引号紧贴汉字的粗体（用户实际遇到的案例）', () => {
    const raw = '在职人员可报销的医院范围可以概括为**“一个基础、两类定点、三种特殊”**：'
    expect(rendersBold(raw)).toBe(true)
    // 修复只加空格，不丢内容
    expect(fixCjkBoldEmphasis(raw).replace(/ /g, '')).toBe(raw.replace(/ /g, ''))
  })

  it('英文直引号同样中招并修复', () => {
    expect(rendersBold('称为**"核心"**模块')).toBe(true)
  })

  it('closing 端紧跟汉字（标点+**+汉字）也修复', () => {
    expect(rendersBold('这就是**“重点”**啊')).toBe(true)
  })

  it('普通粗体、行首粗体不受影响', () => {
    expect(rendersBold('这是**正常粗体**测试')).toBe(true)
    expect(rendersBold('**行首粗体**没问题')).toBe(true)
  })

  it('代码里的 ** 不误伤：围栏代码块原样保留', () => {
    const code = '看这段：\n```\nint x = a**b; // 指针写法\n```\n以及 2**3 次方'
    expect(fixCjkBoldEmphasis(code)).toBe(code)
  })

  it('行内代码 span 内的 ** 不动', () => {
    const raw = '变量 `a**b` 的写法'
    expect(fixCjkBoldEmphasis(raw)).toBe(raw)
  })

  it('缩进代码行不修', () => {
    const raw = '    x = a**b'
    expect(fixCjkBoldEmphasis(raw)).toBe(raw)
  })
})
