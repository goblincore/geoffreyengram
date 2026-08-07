import type { ExtensionAPI } from "@mariozechner/pi-coding-agent";
import { StringEnum } from "@mariozechner/pi-ai";
import { Type } from "@sinclair/typebox";
import { execFile } from "node:child_process";
import { homedir } from "node:os";
import { resolve } from "node:path";

const DUALMEM_RUN = process.env.DUALMEM_RUN || resolve(homedir(), ".config", "dualmem", "bin", "dualmem-run");
const COMMAND_TIMEOUT_MS = 15_000;
const EVENT_TIMEOUT_MS = 10_000;
const OUTPUT_LIMIT = 24 * 1024;
const FAILURE = "dualmem lifecycle unavailable";

type DualMemResponse = {
  action?: "inject_context" | "recorded" | "none";
  context?: string;
  diagnostics?: Array<{ code?: string; message?: string }>;
};

type RunResult = {
  stdout: string;
  diagnostic?: string;
};

function run(args: string[], cwd: string, input?: string, timeout = COMMAND_TIMEOUT_MS): Promise<RunResult> {
  return new Promise((done) => {
    let settled = false;
    const finish = (result: RunResult) => {
      if (settled) return;
      settled = true;
      done(result);
    };
    const child = execFile(DUALMEM_RUN, args, {
      cwd,
      timeout,
      maxBuffer: OUTPUT_LIMIT,
      windowsHide: true,
    }, (error, stdout, stderr) => {
      if (error) {
        finish({ stdout: "", diagnostic: FAILURE });
        return;
      }
      if (Buffer.byteLength(stdout, "utf8") > OUTPUT_LIMIT) {
        finish({ stdout: "", diagnostic: FAILURE });
        return;
      }
      finish({
        stdout: stdout.trim(),
        diagnostic: stderr.trim() ? FAILURE : undefined,
      });
    });
    child.on("error", () => finish({ stdout: "", diagnostic: FAILURE }));
    if (input !== undefined) child.stdin?.end(input);
  });
}

async function submitEvent(
  kind: "file_read" | "file_write" | "session_end",
  cwd: string,
  fields: Record<string, unknown> = {},
): Promise<{ response?: DualMemResponse; diagnostic?: string }> {
  const payload = JSON.stringify({
    schema_version: "1.0",
    kind,
    harness: "pi",
    cwd,
    ...fields,
  });
  const result = await run(["event"], cwd, payload, EVENT_TIMEOUT_MS);
  if (!result.stdout) return { diagnostic: result.diagnostic ?? FAILURE };
  try {
    const response = JSON.parse(result.stdout) as DualMemResponse;
    return {
      response,
      diagnostic: result.diagnostic ?? (response.diagnostics?.length ? FAILURE : undefined),
    };
  } catch {
    return { diagnostic: FAILURE };
  }
}

function reportFailure(ctx: { ui?: { notify(message: string, level: "warning"): void } }, diagnostic?: string) {
  if (diagnostic) ctx.ui?.notify(FAILURE, "warning");
}

function filePath(input: Record<string, unknown>): string | undefined {
  const candidate = input.path ?? input.file_path;
  return typeof candidate === "string" && candidate.length > 0 ? candidate : undefined;
}

export default function dualmemExtension(pi: ExtensionAPI) {
  pi.on("tool_call", async (event, ctx) => {
    if (event.toolName !== "read") return;
    const path = filePath(event.input as Record<string, unknown>);
    if (!path) return;
    const result = await submitEvent("file_read", ctx.cwd, {
      files: [path],
      tool: { name: event.toolName, phase: "pre" },
    });
    reportFailure(ctx, result.diagnostic);
    if (result.response?.action === "inject_context" && result.response.context) {
      pi.sendMessage({
        customType: "dualmem-file-context",
        content: result.response.context,
        display: false,
        details: { source: "dualmem-file-read", file: path },
      }, { deliverAs: "steer" });
    }
  });

  pi.on("tool_result", async (event, ctx) => {
    if (event.toolName !== "edit" && event.toolName !== "write") return;
    const path = filePath(event.input as Record<string, unknown>);
    if (!path) return;
    const result = await submitEvent("file_write", ctx.cwd, {
      files: [path],
      tool: { name: event.toolName, phase: "post" },
    });
    reportFailure(ctx, result.diagnostic);
  });

  pi.on("session_shutdown", async (event, ctx) => {
    const result = await submitEvent("session_end", ctx.cwd, { metadata: { reason: event.reason } });
    reportFailure(ctx, result.diagnostic);
  });

  pi.registerTool({
    name: "dualmem",
    label: "DualMem",
    description: "Search, save, checkpoint, and inspect shared cross-session project memory.",
    promptSnippet: "Search and save cross-session project memory",
    promptGuidelines: [
      "Search memory before broad exploration.",
      "Save durable decisions, warnings, architecture, and debugging findings with associated files.",
      "Use checkpoints for in-progress task continuity.",
    ],
    parameters: Type.Object({
      command: StringEnum([
        "search", "add", "checkpoint", "cochange", "unfold", "context", "consult",
        "recall", "precedent", "search-code", "file-context", "profile", "status",
        "facts", "docs", "map", "explore",
      ] as const),
      args: Type.Optional(Type.Array(Type.String(), { description: "Arguments passed directly without shell parsing" })),
    }),
    async execute(_toolCallId, params, _signal, onUpdate, ctx) {
      const args = [params.command, ...(params.args ?? [])];
      onUpdate?.({ content: [{ type: "text", text: `Running dualmem ${params.command}` }] });
      const result = await run(args, ctx.cwd);
      return {
        content: [{ type: "text", text: result.diagnostic ?? (result.stdout || "(no output)") }],
        details: { command: params.command },
      };
    },
  });
}
