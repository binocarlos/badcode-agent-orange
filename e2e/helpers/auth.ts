const SERVER_URL = process.env.AGENTKIT_SERVER_URL || 'http://localhost:8099'

let cachedToken: string | null = null

export async function getDevToken(): Promise<string> {
  if (cachedToken) return cachedToken
  const res = await fetch(`${SERVER_URL}/dev/token`)
  if (!res.ok) throw new Error(`Failed to get dev token: ${res.status}`)
  const { token } = await res.json() as { token: string }
  cachedToken = token
  return token
}

export function getServerUrl(): string {
  return SERVER_URL
}
