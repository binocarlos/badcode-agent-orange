// ClaudeAgentSdkHarness: wraps the @anthropic-ai/claude-agent-sdk query() loop.
// This is today's agent-service.ts runQuery body lifted behind the Harness interface.
//
// Mechanical substitutions from the original:
//   1. streamService.sendX(queryId, …) → ctx.emit.X(…)       (sessionId/queryId already bound)
//   2. this.abortController / supersede logic → ctx.signal    (control server owns abort)
//   3. this.conversationHistory stays instance-local (already instance-shaped)
//
// See agent-library/docs/12-harness.md §ClaudeAgentSdkHarness.

import { query } from '@anthropic-ai/claude-agent-sdk';
import type { SDKUserMessage } from '@anthropic-ai/claude-agent-sdk';
import * as http from 'node:http';
import * as https from 'node:https';
import * as net from 'node:net';
import { v4 as uuidv4 } from 'uuid';
import { processAttachments, EmbeddedImage } from '../services/attachment-prompt.js';
import type { QueryRequest } from '../types/index.js';
import type { Harness, TurnContext } from './harness.js';
import type { HarnessDescriptor } from './registry.js';

// ---------------------------------------------------------------------------
// Per-session header proxy
//
// The claude-code subprocess (spawned by the SDK) makes API calls to
// ANTHROPIC_BASE_URL directly, bypassing the globalThis.fetch patch that
// would normally inject x-session-id. To ensure the host model proxy can
// identify which session each API call belongs to, we spin up a lightweight
// per-turn HTTP proxy that:
//   1. Listens on a random localhost port
//   2. Forwards all requests to the real ANTHROPIC_BASE_URL
//   3. Injects the x-session-id header on every forwarded request
//
// The subprocess's ANTHROPIC_BASE_URL env var is overridden to point to this
// proxy instead of the real upstream, so every API call passes through it.
// ---------------------------------------------------------------------------

export interface SessionProxy {
  baseURL: string;       // e.g. http://127.0.0.1:<port>
  close: () => void;
}

export function startSessionProxy(sessionId: string, upstreamBaseURL: string): Promise<SessionProxy> {
  return new Promise((resolve, reject) => {
    const upstream = new URL(upstreamBaseURL);
    const isHttps = upstream.protocol === 'https:';
    const upstreamPort = upstream.port
      ? parseInt(upstream.port, 10)
      : isHttps ? 443 : 80;
    // The upstream base URL may carry a path prefix (e.g. <agentd>/agent-proxy);
    // forwarded requests must keep it or they land on the wrong upstream route.
    const upstreamPathPrefix = upstream.pathname.replace(/\/$/, '');

    const server = http.createServer((clientReq, clientRes) => {
      const options: http.RequestOptions = {
        hostname: upstream.hostname,
        port: upstreamPort,
        path: upstreamPathPrefix + (clientReq.url ?? '/'),
        method: clientReq.method,
        headers: {
          ...clientReq.headers,
          'x-session-id': sessionId,
          host: upstream.host,
        },
      };

      const proto = isHttps ? https : http;
      const proxyReq = proto.request(options, (proxyRes) => {
        clientRes.writeHead(proxyRes.statusCode ?? 200, proxyRes.headers);
        proxyRes.pipe(clientRes, { end: true });
      });

      proxyReq.on('error', (err) => {
        console.error(`[SessionProxy] upstream error for session ${sessionId}: ${err.message}`);
        if (!clientRes.headersSent) {
          clientRes.writeHead(502);
        }
        clientRes.end();
      });

      clientReq.pipe(proxyReq, { end: true });
    });

    server.on('error', reject);

    // Listen on random port
    server.listen(0, '127.0.0.1', () => {
      const addr = server.address() as net.AddressInfo;
      const baseURL = `http://127.0.0.1:${addr.port}`;
      console.log(`[SessionProxy] session=${sessionId} listening on ${baseURL} → ${upstreamBaseURL}`);
      resolve({
        baseURL,
        close: () => server.close(),
      });
    });
  });
}

// ---------------------------------------------------------------------------
// Helpers (verbatim from agent-service.ts)
// ---------------------------------------------------------------------------

/**
 * Build an AsyncIterable<SDKUserMessage> that yields a single user message
 * with text + image content blocks. This lets the LLM see PPTX slide images
 * inline without the agent needing to call view_image.
 */
export async function* buildImagePrompt(
  text: string,
  images: EmbeddedImage[],
  sessionId: string,
): AsyncIterable<SDKUserMessage> {
  const contentBlocks: Array<
    | { type: 'text'; text: string }
    | { type: 'image'; source: { type: 'base64'; media_type: string; data: string } }
  > = [];

  contentBlocks.push({ type: 'text', text });

  for (const img of images) {
    contentBlocks.push({
      type: 'image',
      source: { type: 'base64', media_type: img.mimeType, data: img.base64 },
    });
    contentBlocks.push({ type: 'text', text: img.label });
  }

  yield {
    type: 'user' as const,
    message: { role: 'user' as const, content: contentBlocks as never },
    parent_tool_use_id: null,
    session_id: sessionId,
  };
}

// ---------------------------------------------------------------------------
// ClaudeAgentSdkHarness
// ---------------------------------------------------------------------------

export class ClaudeAgentSdkHarness implements Harness {
  readonly name = 'claude-agent-sdk';

  private conversationHistory: Array<{ role: 'user' | 'assistant'; content: string }> = [];

  async runTurn(req: QueryRequest, ctx: TurnContext): Promise<void> {
    const { queryId, sessionId, signal, emit, resolved, config } = ctx;
    console.log(`[ClaudeAgentSdkHarness] Starting turn for query ${queryId}`);

    // Build prompt with conversation history for multi-turn support
    if (this.conversationHistory.length > 0) {
      const historyText = this.conversationHistory
        .map(m => `${m.role === 'user' ? 'Human' : 'Assistant'}: ${m.content}`)
        .join('\n\n');
      const conversationContext = `\n\n## Previous Conversation\n${historyText}\n\n## Current Message\nThe user's current message follows. Respond to it in the context of the previous conversation.`;
      req.systemPrompt = (req.systemPrompt || '') + conversationContext;
    }

    // Track user message in conversation history
    this.conversationHistory.push({ role: 'user', content: req.prompt });

    // Track registered artifact paths to avoid duplicate registration
    const registeredPaths = new Set<string>();

    let messageId = uuidv4();
    let fullContent = '';
    let resolvedModel = req.model || config.DEFAULT_MODEL;
    const pendingToolCalls: string[] = [];
    let hasTodoWrite = false;
    // Declared here (outside try) so the finally block can always close it.
    let sessionProxy: SessionProxy | null = null;

    try {
      // Notify stream listeners that assistant response is starting
      console.log(`[SSE-DEBUG][ClaudeAgentSdkHarness] Emitting message_start for query=${queryId} messageId=${messageId} at ${Date.now()}`);
      emit.messageStart(messageId, 'assistant');

      // Define hooks for tool lifecycle events
      const hooks = {
        PreToolUse: [{
          hooks: [
            async (input: unknown) => {
              const hookInput = input as {
                tool_use_id: string;
                tool_name: string;
                tool_input: unknown;
              };
              console.log(`[ClaudeAgentSdkHarness] PreToolUse hook fired: ${hookInput.tool_name} (${hookInput.tool_use_id})`);

              if (!pendingToolCalls.includes(hookInput.tool_use_id)) {
                console.log(`[ClaudeAgentSdkHarness] Emitting tool_use_start for SDK tool: ${hookInput.tool_name} (${hookInput.tool_use_id})`);
                emit.toolUseStart(
                  hookInput.tool_use_id,
                  hookInput.tool_name,
                  hookInput.tool_input as Record<string, unknown>,
                );
                pendingToolCalls.push(hookInput.tool_use_id);
              }

              // Plan mode enforcement
              if (req.planMode === 'require' && !hasTodoWrite) {
                const executionTools = ['Bash', 'mcp__ui__write_file'];
                if (executionTools.includes(hookInput.tool_name)) {
                  return {
                    decision: 'block' as const,
                    reason: 'Please create a plan using TodoWrite before executing tools.',
                  };
                }
              }

              // Block long-running server commands
              if (hookInput.tool_name === 'Bash') {
                const cmd = (hookInput.tool_input as { command?: string }).command || '';
                const serverPatterns = [
                  /\bvite\s+(preview|dev)\b/,
                  /\bnpx\s+serve\b/,
                  /\bhttp-server\b/,
                  /\bpython3?\s+-m\s+http\.server\b/,
                  /\blive-server\b/,
                  /\bnpm\s+(start|run\s+dev)\b/,
                  /\byarn\s+(start|dev)\b/,
                ];
                if (serverPatterns.some(p => p.test(cmd))) {
                  return {
                    decision: 'block' as const,
                    reason: 'Long-running server commands hang the session. Use screenshot_url with the local file path (e.g. /workspace/dist/index.html) instead — it serves files via HTTP automatically. For web apps, just run `vite build` and then `register_artifact`.',
                  };
                }
              }

              emit.hookEvent('pre_tool', {
                toolUseId: hookInput.tool_use_id,
                toolName: hookInput.tool_name,
                toolInput: hookInput.tool_input,
                timestamp: new Date().toISOString(),
              });
              return { continue: true };
            },
          ],
        }],
        PostToolUse: [{
          hooks: [
            async (input: unknown) => {
              const hookInput = input as {
                tool_use_id: string;
                tool_name: string;
                tool_input: unknown;
                tool_response: unknown;
              };
              console.log(`[ClaudeAgentSdkHarness] PostToolUse hook fired: ${hookInput.tool_name} (${hookInput.tool_use_id})`);
              if (hookInput.tool_name === 'TodoWrite') hasTodoWrite = true;
              const output = typeof hookInput.tool_response === 'string'
                ? hookInput.tool_response
                : JSON.stringify(hookInput.tool_response);
              const resp = hookInput.tool_response as Record<string, unknown> | undefined;
              const isError = !!(resp?.is_error || resp?.isError);
              emit.hookEvent('post_tool', {
                toolUseId: hookInput.tool_use_id,
                toolName: hookInput.tool_name,
                toolInput: hookInput.tool_input,
                toolResponse: hookInput.tool_response,
                timestamp: new Date().toISOString(),
              });
              emit.toolUseEnd(hookInput.tool_use_id, output, isError);
              const idx = pendingToolCalls.indexOf(hookInput.tool_use_id);
              if (idx > -1) pendingToolCalls.splice(idx, 1);

              // Check for special markers in tool response — PLUGIN-DRIVEN dispatch.
              try {
                const responseStr = typeof hookInput.tool_response === 'string'
                  ? hookInput.tool_response
                  : JSON.stringify(hookInput.tool_response);

                let contentBlocks: Array<{ type: string; text: string }> = [];
                let parsedDirect: Record<string, unknown> | null = null;
                try {
                  const parsed = JSON.parse(responseStr);
                  if (parsed.content && Array.isArray(parsed.content)) {
                    contentBlocks = parsed.content.filter((b: Record<string, unknown>) => b.type === 'text' && b.text);
                  } else if (Array.isArray(parsed) && parsed[0]?.text) {
                    contentBlocks = parsed.filter((b: Record<string, unknown>) => b.type === 'text' && b.text);
                  } else if (typeof parsed === 'object' && !Array.isArray(parsed)) {
                    contentBlocks = [{ type: 'text', text: responseStr }];
                    parsedDirect = parsed;
                  }
                } catch { /* ignore nested parse errors */ }

                const markerToolNames = resolved.markers.map(m => m.key.replace(/^__/, ''));
                const isMarkerTool = markerToolNames.some(t => hookInput.tool_name?.includes(t));
                if (isMarkerTool && contentBlocks.length === 0) {
                  console.warn(`[ClaudeAgentSdkHarness] WARNING: Marker tool "${hookInput.tool_name}" returned but no content blocks found. Response preview: ${responseStr.substring(0, 200)}`);
                }

                let hasContentReplacement = false;
                let anyMarkerFound = false;
                const replacementBlocks: Array<{ type: 'text'; text: string }> = [];

                for (const block of contentBlocks) {
                  let blockData: Record<string, unknown> | null = null;
                  if (parsedDirect && contentBlocks.length === 1) {
                    blockData = parsedDirect;
                  } else if (block.text[0] === '{' || block.text[0] === '[') {
                    try { blockData = JSON.parse(block.text); } catch { /* not JSON */ }
                  }

                  let markerHandled = false;
                  if (blockData) {
                    for (const markerSpec of resolved.markers) {
                      if (blockData[markerSpec.key]) {
                        anyMarkerFound = true;
                        markerHandled = true;
                        const eventData = markerSpec.toEvent(blockData);
                        emit.event(markerSpec.event, {
                          ...eventData,
                          toolCallId: hookInput.tool_use_id,
                        });
                        const modelText = markerSpec.toModelText(blockData);
                        replacementBlocks.push({ type: 'text' as const, text: modelText });
                        hasContentReplacement = true;
                        break;
                      }
                    }
                  }

                  if (!markerHandled) {
                    if (blockData?.__artifact_registered) {
                      anyMarkerFound = true;
                      registeredPaths.add(blockData.file_path as string);
                      emit.event('artifact_registered', {
                        filePath: blockData.file_path,
                        label: blockData.label,
                        description: blockData.description,
                        artifactType: blockData.artifact_type,
                        publishToFiles: blockData.publish_to_files || false,
                      });
                    }
                    replacementBlocks.push({ type: 'text' as const, text: block.text });
                  }
                }

                if (isMarkerTool && contentBlocks.length > 0 && !anyMarkerFound) {
                  const firstBlock = contentBlocks[0]?.text || '';
                  let keys = '';
                  try { keys = Object.keys(JSON.parse(firstBlock)).join(', '); } catch { /* ignore */ }
                  console.warn(`[ClaudeAgentSdkHarness] WARNING: Marker tool "${hookInput.tool_name}" returned data but no known marker key found. Keys: ${keys}`);
                }

                if (hookInput.tool_name === 'Bash') {
                  const cmd = (hookInput.tool_input as { command?: string }).command || '';
                  const outputStr = typeof hookInput.tool_response === 'string'
                    ? hookInput.tool_response
                    : JSON.stringify(hookInput.tool_response);

                  if (/✓ built in [\d.]+s/.test(outputStr)) {
                    const htmlMatch = outputStr.match(/([^\s"']+\/dist\/index\.html)/);
                    if (htmlMatch && !registeredPaths.has(htmlMatch[1])) {
                      registeredPaths.add(htmlMatch[1]);
                      console.log(`[ClaudeAgentSdkHarness] Auto-registering webapp artifact from vite build: ${htmlMatch[1]}`);
                      emit.event('artifact_registered', {
                        filePath: htmlMatch[1],
                        label: 'Interactive Visualization',
                        description: 'Web application built with Vite',
                        artifactType: 'webapp',
                      });
                    }
                  }

                  const pyMatch = cmd.match(/python3?\s+(\/workspace\/[\w./-]+\.py)\b/);
                  if (pyMatch && !registeredPaths.has(pyMatch[1].replace(/^\/workspace\//, ''))) {
                    const relativePath = pyMatch[1].replace(/^\/workspace\//, '');
                    registeredPaths.add(relativePath);
                    const fileName = relativePath.split('/').pop() || relativePath;
                    console.log(`[ClaudeAgentSdkHarness] Auto-registering Python script artifact: ${relativePath}`);
                    emit.event('artifact_registered', {
                      filePath: relativePath,
                      label: fileName,
                      description: 'Python analysis script',
                      artifactType: 'code',
                    });
                  }
                }

                if (hasContentReplacement) {
                  const combinedText = replacementBlocks.map(b => b.text).join('\n\n');
                  return {
                    continue: true,
                    hookSpecificOutput: {
                      hookEventName: 'PostToolUse' as const,
                      updatedMCPToolOutput: combinedText,
                    },
                  };
                }
              } catch { /* ignore marker detection errors */ }

              return { continue: true };
            },
          ],
        }],
        Notification: [{
          hooks: [
            async (input: unknown) => {
              const hookInput = input as {
                message: string;
                title?: string;
                notification_type?: string;
              };
              console.log(`[ClaudeAgentSdkHarness] Notification hook fired: ${hookInput.message}`);
              emit.hookEvent('notification', {
                message: hookInput.message,
                title: hookInput.title,
                notificationType: hookInput.notification_type,
                timestamp: new Date().toISOString(),
              });
              return { continue: true };
            },
          ],
        }],
        SubagentStart: [{
          hooks: [
            async (input: unknown) => {
              const hookInput = input as {
                agent_id: string;
                agent_type?: string;
              };
              console.log(`[ClaudeAgentSdkHarness] SubagentStart hook fired: ${hookInput.agent_id} (${hookInput.agent_type})`);
              emit.subagentEvent('start', hookInput.agent_id, hookInput.agent_type);
              return { continue: true };
            },
          ],
        }],
        SubagentStop: [{
          hooks: [
            async (input: unknown) => {
              const hookInput = input as {
                agent_id: string;
                result?: string;
              };
              console.log(`[ClaudeAgentSdkHarness] SubagentStop hook fired: ${hookInput.agent_id}`);
              emit.subagentEvent('stop', hookInput.agent_id, undefined, hookInput.result);
              return { continue: true };
            },
          ],
        }],
      };

      // Build prompt with optional embedded images for PPTX slides.
      let promptInput: string | AsyncIterable<SDKUserMessage> = req.prompt;
      let promptLength = req.prompt.length;
      if (req.attachments && req.attachments.length > 0) {
        const progress = (phase: string, label: string) => {
          emit.activityUpdate(phase, label);
        };
        const { promptParts, embeddedImages, renderedSlides } = await processAttachments(
          req.attachments,
          undefined,
          progress,
        );
        const textPrompt = [req.prompt, ...promptParts].join('\n\n');
        promptLength = textPrompt.length;

        for (let i = 0; i < renderedSlides.length; i++) {
          const relativePath = renderedSlides[i].replace(/^\/workspace\//, '');
          emit.event('artifact_registered', {
            filePath: relativePath,
            label: `Slide ${i + 1}`,
            description: 'Extracted from uploaded PowerPoint',
            artifactType: 'image',
          });
        }

        if (embeddedImages.length > 0) {
          promptInput = buildImagePrompt(textPrompt, embeddedImages, sessionId);
          console.log(`[ClaudeAgentSdkHarness] Built multi-modal prompt with ${embeddedImages.length} embedded images`);
        } else {
          promptInput = textPrompt;
        }
        console.log(`[ClaudeAgentSdkHarness] Built prompt with ${req.attachments.length} attachment(s)`);
      }

      console.log(`[ClaudeAgentSdkHarness] Calling Claude Agent SDK query() with model: ${req.model || config.DEFAULT_MODEL}`);
      console.log(`[ClaudeAgentSdkHarness] Prompt length: ${promptLength} chars`);
      console.log(`[ClaudeAgentSdkHarness] Allowed tools: ${resolved.allowedTools.join(', ')}`);

      // Create an AbortController that bridges ctx.signal → the SDK's abortController
      const abortController = new AbortController();
      signal.addEventListener('abort', () => abortController.abort(signal.reason), { once: true });

      // Start a per-session HTTP proxy so that the claude subprocess's outbound
      // API calls are tagged with the correct x-session-id header. The subprocess
      // inherits process.env, so we temporarily override ANTHROPIC_BASE_URL to
      // point to the proxy instead of the real upstream.
      const subprocessEnv: Record<string, string> = { ...process.env as Record<string, string> };
      if (config.ANTHROPIC_BASE_URL) {
        try {
          sessionProxy = await startSessionProxy(sessionId, config.ANTHROPIC_BASE_URL);
          subprocessEnv['ANTHROPIC_BASE_URL'] = sessionProxy.baseURL;
        } catch (err) {
          console.warn(`[ClaudeAgentSdkHarness] Failed to start session proxy, continuing without it: ${err}`);
        }
      } else if (subprocessEnv['CLAUDE_CODE_OAUTH_TOKEN']) {
        // Direct-to-Anthropic subscription mode: the CLI must authenticate with
        // the OAuth token, so make sure no leftover ANTHROPIC_API_KEY (dummy or
        // session JWT) can shadow it.
        delete subprocessEnv['ANTHROPIC_API_KEY'];
      }

      console.log(`[SSE-DEBUG][ClaudeAgentSdkHarness] Entering SDK for-await loop at ${Date.now()}`);
      for await (const message of query({
        prompt: promptInput,
        options: {
          abortController,
          cwd: '/workspace',
          model: req.model || config.DEFAULT_MODEL,
          maxTurns: req.maxTurns || config.DEFAULT_MAX_TURNS,
          maxThinkingTokens: config.DEFAULT_THINKING_BUDGET_TOKENS,
          systemPrompt: req.systemPrompt
            ? { type: 'preset' as const, preset: 'claude_code' as const, append: req.systemPrompt }
            : undefined,
          settingSources: ['project'],
          allowedTools: resolved.allowedTools,
          disallowedTools: resolved.disallowedTools,
          mcpServers: resolved.mcpServers,
          permissionMode: 'bypassPermissions',
          allowDangerouslySkipPermissions: true,
          includePartialMessages: true,
          persistSession: false,
          hooks,
          env: subprocessEnv,
        },
      })) {
        console.log(`[ClaudeAgentSdkHarness] SDK message type: ${message.type}`);

        if (message.type === 'stream_event') {
          const event = message.event;
          if (event.type === 'content_block_delta') {
            const delta = event.delta;
            if (delta.type === 'text_delta' && delta.text) {
              console.log(`[SSE-DEBUG][ClaudeAgentSdkHarness] content_block_delta received: len=${delta.text.length} preview="${delta.text.substring(0, 40)}" at ${Date.now()}`);
              fullContent += delta.text;
              emit.contentDelta(messageId, delta.text);
            } else if (delta.type === 'thinking_delta' && 'thinking' in delta) {
              emit.thinkingDelta(messageId, (delta as { thinking: string }).thinking);
            } else if (delta.type === 'input_json_delta' && delta.partial_json) {
              console.log(`[ClaudeAgentSdkHarness] Tool input streaming: ${delta.partial_json.substring(0, 50)}...`);
              emit.toolInputDelta(delta.partial_json);
            }
          } else if (event.type === 'content_block_start') {
            const block = event.content_block;
            if (block.type === 'tool_use') {
              console.log(`[ClaudeAgentSdkHarness] Tool use starting: ${block.name} (${block.id})`);
              emit.activityUpdate('preparing_tool', `Preparing: ${block.name}`, { toolName: block.name });
            } else if (block.type === 'text') {
              emit.activityUpdate('writing', 'Composing response...');
            } else if ((block as { type: string }).type === 'thinking') {
              emit.activityUpdate('thinking', 'Reasoning...');
            }
          } else if (event.type === 'content_block_stop') {
            emit.activityUpdate('processing', 'Processing...');
          } else if (event.type === 'message_stop') {
            emit.activityUpdate('processing_results', 'Processing results...');
          }
        } else if (message.type === 'assistant') {
          const apiMessage = message.message;
          for (const block of apiMessage.content) {
            if (block.type === 'tool_use') {
              console.log(`[ClaudeAgentSdkHarness] Sending tool_use_start for: ${block.name} (${block.id})`);
              emit.toolUseStart(block.id, block.name, block.input as Record<string, unknown>);
              pendingToolCalls.push(block.id);
            }
          }
        } else if (message.type === 'user') {
          const userMessage = message.message;
          for (const block of userMessage.content) {
            if (typeof block !== 'string' && block.type === 'tool_result') {
              const resultBlock = block as { tool_use_id: string; content: unknown; is_error?: boolean };
              console.log(`[ClaudeAgentSdkHarness] Tool result received for: ${resultBlock.tool_use_id} (is_error=${resultBlock.is_error})`);
              if (pendingToolCalls.includes(resultBlock.tool_use_id)) {
                const output = typeof resultBlock.content === 'string'
                  ? resultBlock.content
                  : JSON.stringify(resultBlock.content);
                emit.toolUseEnd(resultBlock.tool_use_id, output, resultBlock.is_error);
                const idx = pendingToolCalls.indexOf(resultBlock.tool_use_id);
                if (idx > -1) pendingToolCalls.splice(idx, 1);
              }
            }
          }

          emit.messageEnd(messageId);
          messageId = uuidv4();
          emit.messageStart(messageId, 'assistant');
        } else if (message.type === 'tool_progress') {
          const progressMsg = message as {
            type: 'tool_progress';
            tool_use_id: string;
            tool_name: string;
            elapsed_time_seconds: number;
            parent_tool_use_id?: string | null;
          };
          console.log(`[ClaudeAgentSdkHarness] Tool progress: ${progressMsg.tool_name} - ${progressMsg.elapsed_time_seconds}s`);
          emit.toolProgress(
            progressMsg.tool_use_id,
            progressMsg.tool_name,
            progressMsg.elapsed_time_seconds,
            progressMsg.parent_tool_use_id || null,
          );
        } else if (message.type === 'system') {
          const sysMsg = message as {
            type: 'system';
            subtype: string;
            tools?: string[];
            model?: string;
            mcp_servers?: { name: string; status: string }[];
            status?: string;
            compact_metadata?: { trigger: string; pre_tokens: number };
            hook_name?: string;
            hook_event?: string;
            stdout?: string;
            stderr?: string;
            exit_code?: number;
          };
          console.log(`[ClaudeAgentSdkHarness] System message: ${sysMsg.subtype}`);

          if (sysMsg.subtype === 'init') {
            if (sysMsg.model) {
              resolvedModel = sysMsg.model;
            }
            emit.sessionInfo({
              tools: sysMsg.tools || [],
              model: resolvedModel,
              mcpServers: sysMsg.mcp_servers || [],
            });
          } else if (sysMsg.subtype === 'status') {
            emit.systemStatus(
              (sysMsg.status as 'init' | 'compacting' | 'ready' | 'auth') || 'ready',
            );
          } else if (sysMsg.subtype === 'compact_boundary') {
            emit.systemStatus('compacting', {
              trigger: sysMsg.compact_metadata?.trigger,
              preTokens: sysMsg.compact_metadata?.pre_tokens,
            });
          } else if (sysMsg.subtype === 'hook_response') {
            emit.hookEvent('hook_response', {
              hookName: sysMsg.hook_name,
              hookEvent: sysMsg.hook_event,
              stdout: sysMsg.stdout,
              stderr: sysMsg.stderr,
              exitCode: sysMsg.exit_code,
            });
          }
        } else if (message.type === 'auth_status') {
          const authMsg = message as {
            type: 'auth_status';
            isAuthenticating?: boolean;
            output?: string[];
            error?: string;
          };
          console.log(`[ClaudeAgentSdkHarness] Auth status: authenticating=${authMsg.isAuthenticating}`);
          emit.systemStatus('auth', {
            isAuthenticating: authMsg.isAuthenticating,
            output: authMsg.output?.join('\n'),
            error: authMsg.error,
          });
        } else if (message.type === 'result') {
          console.log(`[SSE-DEBUG][ClaudeAgentSdkHarness] Query result received: subtype=${message.subtype} fullContentLen=${fullContent.length} at ${Date.now()}`);
          const resultContent = message.subtype === 'success' ? message.result : fullContent;

          emit.messageEnd(messageId);

          if (message.subtype === 'success') {
            console.log(`[ClaudeAgentSdkHarness] Query completed successfully`);
            if (resultContent) {
              this.conversationHistory.push({ role: 'assistant', content: resultContent });
            }
            emit.endQuery(
              'completed',
              resultContent,
              message.total_cost_usd,
              {
                inputTokens: message.usage.input_tokens,
                outputTokens: message.usage.output_tokens,
              },
              resolvedModel,
            );
          } else {
            const errorMsg = message.errors?.join(', ') || 'Unknown error';
            console.error(`[ClaudeAgentSdkHarness] Query completed with error: ${errorMsg}`);
            emit.endQuery('error', undefined, undefined, undefined, resolvedModel);
          }
        }
      }
    } catch (error) {
      const errorMsg = error instanceof Error ? error.message : 'Unknown error';
      const errorStack = error instanceof Error ? error.stack : undefined;
      console.error(`[ClaudeAgentSdkHarness] Agent query error: ${errorMsg}`);
      console.error('Stack:', errorStack);

      emit.error('AGENT_ERROR', errorMsg);
      emit.endQuery('error', undefined, undefined, undefined, resolvedModel);
    } finally {
      // Always close the per-session proxy, whether the query succeeded or failed.
      sessionProxy?.close();
    }
  }

  loadConversation(messages: Array<{ role: 'user' | 'assistant'; content: string }>): void {
    console.log(`[ClaudeAgentSdkHarness] Loading ${messages.length} messages into conversation history`);
    this.conversationHistory = messages.map(m => ({ role: m.role, content: m.content }));
  }

  resetConversation(): void {
    console.log(`[ClaudeAgentSdkHarness] Clearing ${this.conversationHistory.length} messages`);
    this.conversationHistory = [];
  }
}

// ---------------------------------------------------------------------------
// HarnessDescriptor for registration
// ---------------------------------------------------------------------------

export const claudeAgentSdkDescriptor: HarnessDescriptor = {
  name: 'claude-agent-sdk',
  credentials: {
    requiredEnv: [],
    anyOfEnv: ['ANTHROPIC_BASE_URL', 'CLAUDE_CODE_OAUTH_TOKEN', 'ANTHROPIC_API_KEY'],
    describe: () =>
      'Claude Agent SDK needs a model credential: ANTHROPIC_BASE_URL (host model proxy), ' +
      'CLAUDE_CODE_OAUTH_TOKEN (subscription), or ANTHROPIC_API_KEY (direct API)',
  },
  create(_sessionId: string): ClaudeAgentSdkHarness {
    return new ClaudeAgentSdkHarness();
  },
};
