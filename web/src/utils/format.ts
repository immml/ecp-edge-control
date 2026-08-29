/** 字节数转人类可读单位。用 1024 进制，与 Linux 的 df -h 保持一致。 */
export function formatBytes(bytes: number, digits = 1): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** i
  return `${value.toFixed(i === 0 ? 0 : digits)} ${units[i]}`
}

/** 毫秒摄氏度转摄氏度（Linux thermal zone 的惯例单位） */
export function millidegToCelsius(mc: number): number {
  return Math.round((mc / 1000) * 10) / 10
}

/** 相对时间，如"3 分钟前" */
export function timeAgo(iso: string): string {
  const ts = new Date(iso).getTime()
  if (Number.isNaN(ts)) return '-'
  const diff = Date.now() - ts
  const sec = Math.floor(diff / 1000)
  if (sec < 60) return `${sec} 秒前`
  const min = Math.floor(sec / 60)
  if (min < 60) return `${min} 分钟前`
  const hour = Math.floor(min / 60)
  if (hour < 24) return `${hour} 小时前`
  return `${Math.floor(hour / 24)} 天前`
}

/** 百分比，用于指标条 */
export function percent(used: number, total: number): number {
  if (!total) return 0
  return Math.round((used / total) * 1000) / 10
}
