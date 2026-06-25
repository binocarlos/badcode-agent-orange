import { readFileSync, unlinkSync } from 'node:fs'
import path from 'node:path'

export default async function globalTeardown() {
  const pidFile = path.join(import.meta.dirname, '.pids')
  try {
    const pids = readFileSync(pidFile, 'utf-8').trim().split('\n').map(Number)
    for (const pid of pids) {
      try { process.kill(pid, 'SIGTERM') } catch {}
    }
    unlinkSync(pidFile)
  } catch {}
}
