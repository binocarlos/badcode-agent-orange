import { z } from 'zod';

export const ConfigSchema = z.object({
  // Server
  PORT: z.string().default('3010').transform(Number),
  HOST: z.string().default('0.0.0.0'),
  NODE_ENV: z.enum(['development', 'production', 'test']).default('development'),

  // Anthropic (via host model proxy -- no real API key in sandbox)
  ANTHROPIC_BASE_URL: z.string().min(1, 'ANTHROPIC_BASE_URL is required (points to host model proxy)'),

  // Host API for tool callbacks.
  // HOST_API_URL is the canonical library name.
  HOST_API_URL: z.string().default('http://localhost:80/api/v1'),

  // Session identity (injected by the ExecutionEnvironment when provisioning the sandbox).
  // Optional in multi-session mode — sessions are created per-request via POST /sessions.
  // Kept for back-compat with single-session deployments where they are still set.
  SESSION_TOKEN: z.string().optional().default(''),
  SESSION_ID: z.string().optional().default(''),
  SESSION_CUSTOMER: z.string().default(''),
  SESSION_JOB: z.string().default(''), // empty = cross-job session (all jobs)

  // Optional directory of product tool-plugin modules. On startup the sandbox
  // dynamically imports every *.js/*.ts file here and registers each exported
  // ToolPlugin. Empty (default) = builtins only. Generic: the library names no
  // product tools — a host image populates this directory.
  PRODUCT_PLUGINS_DIR: z.string().optional().default(''),

  // Logging
  LOG_LEVEL: z.enum(['trace', 'debug', 'info', 'warn', 'error', 'fatal']).default('info'),

  // Agent defaults
  DEFAULT_MODEL: z.string().default('claude-opus-4-5'),
  DEFAULT_MAX_TURNS: z.string().default('100').transform(Number),
  DEFAULT_THINKING_BUDGET_TOKENS: z.string().default('10000').transform(Number),
});

export type Config = z.infer<typeof ConfigSchema>;

function loadConfig(): Config {
  const env = {
    PORT: process.env.PORT,
    HOST: process.env.HOST,
    NODE_ENV: process.env.NODE_ENV,
    ANTHROPIC_BASE_URL: process.env.ANTHROPIC_BASE_URL,
    HOST_API_URL: process.env.HOST_API_URL,
    SESSION_TOKEN: process.env.SESSION_TOKEN,
    SESSION_ID: process.env.SESSION_ID,
    SESSION_CUSTOMER: process.env.SESSION_CUSTOMER,
    SESSION_JOB: process.env.SESSION_JOB,
    PRODUCT_PLUGINS_DIR: process.env.PRODUCT_PLUGINS_DIR,
    LOG_LEVEL: process.env.LOG_LEVEL,
    DEFAULT_MODEL: process.env.DEFAULT_MODEL,
    DEFAULT_MAX_TURNS: process.env.DEFAULT_MAX_TURNS,
    DEFAULT_THINKING_BUDGET_TOKENS: process.env.DEFAULT_THINKING_BUDGET_TOKENS,
  };

  const result = ConfigSchema.safeParse(env);

  if (!result.success) {
    console.error('Configuration validation failed:');
    console.error(result.error.format());
    process.exit(1);
  }

  return result.data;
}

export const config = loadConfig();
