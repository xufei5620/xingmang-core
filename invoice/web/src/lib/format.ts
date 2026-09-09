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

// CR-0009 (XM-INV-CR0009-LEDGER-VIEW): the account ledger view's own
// timestamps (last_checkpoint_at, last_reconciled_at, policy_start_at) must
// display in Asia/Shanghai specifically (invoice_eligibility_policy's own
// display timezone), unlike dateTime() above, which uses the browser's
// local timezone. A dedicated helper rather than changing dateTime()
// itself, which every other page already relies on unchanged.
export const dateTimeShanghai = (value: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
    timeZone: 'Asia/Shanghai',
  }).format(new Date(value))

// The backend sends a "never observed yet" source account timestamp as a
// real (non-omitted) time value rather than leaving the field absent: Go's
// zero time.Time marshals as 0001-01-01, and some queries instead coalesce a
// missing value to the Unix epoch before it's normalized server-side. Either
// shape must render as "no sync yet", not as a formatted 0001/1970 date.
export const isUnobservedTimestamp = (value: string) =>
  value.startsWith('0001-01-01') || value.startsWith('1970-01-01')

export const maskTaxId = (value: string) => {
  if (!value) return '个人抬头'
  if (value.length < 8) return value
  return `${value.slice(0, 4)} **** **** ${value.slice(-4)}`
}
