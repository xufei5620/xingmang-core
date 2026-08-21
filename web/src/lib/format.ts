export const money = (minor: number) =>
  new Intl.NumberFormat('zh-CN', {
    style: 'currency',
    currency: 'CNY',
    minimumFractionDigits: 2,
  }).format(minor / 100)

export const dateTime = (value: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  }).format(new Date(value))

export const maskTaxId = (value: string) => {
  if (!value) return '个人抬头'
  if (value.length < 8) return value
  return `${value.slice(0, 4)} **** **** ${value.slice(-4)}`
}
