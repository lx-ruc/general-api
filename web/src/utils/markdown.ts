import { marked } from 'marked'
import DOMPurify from 'dompurify'

// Markdown → 安全 HTML：模型输出属不可信内容，渲染后必须经 DOMPurify 消毒
marked.setOptions({ gfm: true, breaks: true })

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
  const html = DOMPurify.sanitize(marked.parse(text, { async: false }), {
    FORBID_TAGS: ['style', 'iframe', 'form', 'input', 'button'],
  })
  if (mdCache.size > 300) mdCache.clear()
  mdCache.set(text, html)
  return html
}
