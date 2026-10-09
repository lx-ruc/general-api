import { marked } from 'marked'
import DOMPurify from 'dompurify'

// Markdown → 安全 HTML：模型输出属不可信内容，渲染后必须经 DOMPurify 消毒
marked.setOptions({ gfm: true, breaks: true })

// CommonMark 的 emphasis 界定符规则对中文不友好：`概括为**"X"**：` 里开头的 ** 左邻是
// 汉字、右邻是标点（引号/冒号），按规范不算 left-flanking，于是整段粗体失效、星号原样
// 露出（英文 "x" 前有空格所以没事）。修复：在这两类组合的 ** 旁插入一个普通空格恢复
// flanking 资格——实测零宽空格无效（CommonMark 不把 U+200B 当空白）。
//   ① 字母/汉字 + `**` + 标点   → 补 opener：`为**"` → `为 **"`
//   ② 标点 + `**` + 字母/汉字   → 补 closer：`"**啊` → `"** 啊`
// 代码内容不修：围栏代码块整体跳过；行内按 ` 分段只处理 code span 外的部分。
const reBoldOpenerGap = /([^\s\p{P}\p{S}])(\*\*)(?=[\p{P}\p{S}])/gu
const reBoldCloserGap = /(?<=[\p{P}\p{S}])(\*\*)(?=[^\s\p{P}\p{S}])/gu

function fixSegment(s: string): string {
  return s.replace(reBoldOpenerGap, '$1 $2').replace(reBoldCloserGap, '$1 ')
}

// fixCjkBoldEmphasis 修中文场景的 ** 粗体界定符失效（marked 实测驱动，见上方注释）
export function fixCjkBoldEmphasis(text: string): string {
  const out: string[] = []
  let inFence = false
  for (const line of text.split('\n')) {
    if (/^\s*(```|~~~)/.test(line)) {
      inFence = !inFence
      out.push(line)
      continue
    }
    if (inFence || /^\s{4,}\S/.test(line)) {
      out.push(line) // 围栏内 / 缩进代码行原样保留
      continue
    }
    // 行内 code span（` 分段，奇数索引为代码）外的文本段才修
    out.push(line.split('`').map((seg, i) => (i % 2 === 0 ? fixSegment(seg) : seg)).join('`'))
  }
  return out.join('\n')
}

// 链接统一新窗口打开（DOMPurify 官方推荐的 hook 写法）
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

// 渲染结果缓存：流式期间每帧重渲染整列消息，历史消息内容不变可直接复用
const mdCache = new Map<string, string>()

export function renderMarkdown(text: string): string {
  const hit = mdCache.get(text)
  if (hit) return hit
  const html = DOMPurify.sanitize(marked.parse(fixCjkBoldEmphasis(text), { async: false }), {
    FORBID_TAGS: ['style', 'iframe', 'form', 'input', 'button'],
  })
  if (mdCache.size > 300) mdCache.clear()
  mdCache.set(text, html)
  return html
}
