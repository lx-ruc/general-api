import { ref, onUnmounted } from 'vue'

/**
 * 验证码/重发 60 秒倒计时（与后端重发窗口同口径）。
 * LoginView/RegisterView 里的手写计时器是历史内联，新页面一律用这里。
 */
export function useCountdown(seconds = 60) {
  const countdown = ref(0)
  let timer: ReturnType<typeof setInterval> | null = null

  function stop() {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  function start(total = seconds) {
    stop()
    countdown.value = total
    timer = setInterval(() => {
      countdown.value--
      if (countdown.value <= 0) stop()
    }, 1000)
  }

  onUnmounted(stop)
  return { countdown, start, stop }
}
