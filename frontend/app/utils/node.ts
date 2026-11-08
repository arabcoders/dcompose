export const formatMemoryBytes = (bytes: number): string => {
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const unit =
    0 < bytes ? Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1) : 0
  return `${new Intl.NumberFormat('en', { maximumFractionDigits: 1 }).format(bytes / 1024 ** unit)} ${units[unit]}`
}

export const formatAgentUptime = (startedAt: string, now = Date.now()): string => {
  const elapsed = now - Date.parse(startedAt)
  if (!Number.isFinite(elapsed) || 0 > elapsed) {
    return 'Unavailable'
  }
  const minutes = Math.floor(elapsed / 60_000)
  const days = Math.floor(minutes / 1440)
  const hours = Math.floor((minutes % 1440) / 60)
  return `${0 < days ? `${days}d ` : ''}${hours}h ${minutes % 60}m`
}
