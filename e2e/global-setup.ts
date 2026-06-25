import { spawn, type ChildProcess } from 'node:child_process'
import { writeFileSync } from 'node:fs'
import path from 'node:path'

const ROOT = path.resolve(import.meta.dirname, '..')
const MOCK_PORT = process.env.MOCK_SERVER_PORT || '4010'
const SERVER_PORT = '8099'
const WEB_PORT = '5173'

const processes: ChildProcess[] = []

async function waitForHealth(url: string, timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url)
      if (res.ok) return
    } catch {}
    await new Promise((r) => setTimeout(r, 500))
  }
  throw new Error(`Health check timeout: ${url}`)
}

export default async function globalSetup() {
  // 1. Mock server
  const mock = spawn('npx', ['tsx', 'src/index.ts'], {
    cwd: path.join(ROOT, 'mock-server'),
    env: { ...process.env, MOCK_SERVER_PORT: MOCK_PORT },
    stdio: 'pipe',
  })
  processes.push(mock)

  // 2. Go example server
  const server = spawn('go', ['run', './cmd/agentd'], {
    cwd: path.join(ROOT, 'go'),
    env: {
      ...process.env,
      ADDR: `:${SERVER_PORT}`,
      DOCKER_HOST: process.env.DOCKER_HOST || 'tcp://localhost:2375',
      AGENTKIT_IMAGE: process.env.AGENTKIT_IMAGE || 'agentkit-sandbox:dev',
      ANTHROPIC_BASE_URL: `http://172.17.0.1:${MOCK_PORT}`,
    },
    stdio: 'pipe',
  })
  processes.push(server)

  // 3. Web dev server
  const web = spawn('npx', ['vite', '--port', WEB_PORT], {
    cwd: path.join(ROOT, 'examples', 'web'),
    env: {
      ...process.env,
      VITE_API: `http://localhost:${SERVER_PORT}`,
      VITE_DEMO_TOKEN: 'dev',
    },
    stdio: 'pipe',
  })
  processes.push(web)

  // Wait for all services
  console.log('Waiting for mock server...')
  await waitForHealth(`http://localhost:${MOCK_PORT}/health`)

  console.log('Waiting for Go server...')
  await waitForHealth(`http://localhost:${SERVER_PORT}/health`, 60_000)

  console.log('Waiting for web dev server...')
  await waitForHealth(`http://localhost:${WEB_PORT}`, 30_000)

  console.log('All services ready.')

  // Store PIDs for teardown
  const pids = processes.map((p) => p.pid).filter(Boolean)
  writeFileSync(path.join(ROOT, 'e2e', '.pids'), pids.join('\n'))
}
