export const apiProxyTarget = (value: string): string => {
  const target = new URL(value)
  // The incoming path already includes /api; retain any deployment prefix only.
  target.pathname = target.pathname.replace(/\/api\/?$/, '/')
  return target.href
}
