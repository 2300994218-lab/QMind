// Read dates in the viewer's own timezone: toISOString() converts to UTC, which
// turns "today" into "yesterday" for every request made before 08:00 in China.
export function localDateStr(date = new Date()) {
  const d = date instanceof Date ? date : new Date(date)
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${d.getFullYear()}-${month}-${day}`
}
