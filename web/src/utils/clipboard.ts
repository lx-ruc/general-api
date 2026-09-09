// 复制到剪贴板：优先 Clipboard API（仅 HTTPS / localhost 可用），
// 非 secure context（如 HTTP 裸 IP 访问）回退隐藏 textarea + execCommand
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 权限被拒等场景，走下面的回退
    }
  }
  const ta = document.createElement('textarea')
  ta.value = text
  // 移出可视区且不占布局，避免页面跳动；保留 focus 能力
  ta.style.position = 'fixed'
  ta.style.top = '-9999px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.focus()
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok
}
