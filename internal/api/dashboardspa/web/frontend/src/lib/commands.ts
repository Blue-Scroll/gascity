import { useEffect, useState } from 'react';

// The "/" list in the composer, built to match what the agent's own Claude Code
// shows when "/" is typed in its tmux pane.
//
// Two halves. The built-in commands are compiled into Claude Code, so the page
// carries them (BUILTIN below). The skills and custom commands live in folders
// on the machine the agent runs on, and which ones an agent sees depends on
// where it was started, so the dashboard's local sidecar reads them per pane at
// /commands (see "the composer's slash list" in gas-city's
// assets/scripts/dashboard-upload.py). A machine without the sidecar answers
// 404 here, and the list is then the built-ins alone.
//
// The agent interprets whatever is sent, so this list is an affordance rather
// than a contract: a command it does not know still goes through, the list only
// saves the typing.
const COMMANDS = '/commands';

export type SlashSource = 'builtin' | 'user' | 'project' | 'plugin';

export interface SlashCommand {
  name: string; // without the leading slash
  description: string;
  source: SlashSource;
}

// Claude Code's built-in commands, minus the ones that do harm when tapped on a
// phone: /exit and /quit end the agent's process, /login and /logout change the
// one Claude login every agent on the machine shares, /resume swaps out the
// conversation gc is tracking, and /memory, /ide, /terminal-setup and
// /install-github-app open editors or setup flows meant for a local terminal.
// Anything missing here can still be typed in full.
const BUILTIN: ReadonlyArray<Omit<SlashCommand, 'source'>> = [
  { name: 'add-dir', description: 'Add another working folder' },
  { name: 'agents', description: 'Manage subagents' },
  { name: 'clear', description: 'Start a fresh context' },
  { name: 'code-review', description: 'Review the current diff for bugs' },
  { name: 'compact', description: 'Summarize and shrink the context' },
  { name: 'config', description: 'Open settings' },
  { name: 'context', description: 'What is in the context window' },
  { name: 'cost', description: 'Token spend for this session' },
  { name: 'doctor', description: 'Check the Claude Code install' },
  { name: 'export', description: 'Save the conversation to a file' },
  { name: 'help', description: 'List commands' },
  { name: 'hooks', description: 'Manage hooks' },
  { name: 'init', description: 'Write a CLAUDE.md for this project' },
  { name: 'loop', description: 'Run a prompt again on an interval' },
  { name: 'mcp', description: 'Manage MCP servers' },
  { name: 'model', description: 'Switch model' },
  { name: 'permissions', description: 'Manage tool permissions' },
  { name: 'plugin', description: 'Manage plugins' },
  { name: 'pr-comments', description: 'Read the comments on a pull request' },
  { name: 'release-notes', description: 'What changed in this Claude Code' },
  { name: 'rewind', description: 'Go back to an earlier point' },
  { name: 'security-review', description: 'Security review of the pending changes' },
  { name: 'simplify', description: 'Clean up the changed code' },
  { name: 'status', description: 'Session and account status' },
  { name: 'usage', description: 'Limits and usage' },
];

export const BUILTIN_COMMANDS: ReadonlyArray<SlashCommand> = BUILTIN.map((c) => ({
  ...c,
  source: 'builtin',
}));

const SOURCES: ReadonlySet<string> = new Set(['user', 'project', 'plugin']);

// The rows the sidecar sends, kept only when they have the shape promised. A
// row that does not is dropped rather than drawn half-filled.
function parseCommands(body: unknown): SlashCommand[] {
  const rows = (body as { commands?: unknown } | null)?.commands;
  if (!Array.isArray(rows)) return [];
  return rows.flatMap((r: unknown) => {
    const row = r as Partial<Record<keyof SlashCommand, unknown>> | null;
    if (!row || typeof row.name !== 'string' || row.name === '') return [];
    const source = typeof row.source === 'string' && SOURCES.has(row.source) ? row.source : 'user';
    return [
      {
        name: row.name,
        description: typeof row.description === 'string' ? row.description : '',
        source: source as SlashSource,
      },
    ];
  });
}

// The agent's own skills and custom commands. An empty list when the sidecar is
// not there or the pane is not addressable: the built-ins still work.
export async function readSessionCommands(
  session: string,
  signal?: AbortSignal,
): Promise<SlashCommand[]> {
  const q = new URLSearchParams({ session });
  const res = await fetch(`${COMMANDS}?${q.toString()}`, {
    // The marker every sidecar request carries: a cross-site request cannot add
    // a custom header without a preflight the gate never grants.
    headers: { 'X-GC-Request': '1' },
    cache: 'no-store',
    ...(signal ? { signal } : {}),
  });
  if (!res.ok) return [];
  return parseCommands(await res.json());
}

// Built-ins first, then the agent's own. A skill named like a built-in does not
// hide the built-in, the same as in the pane.
export function mergeCommands(
  builtins: ReadonlyArray<SlashCommand>,
  own: ReadonlyArray<SlashCommand>,
): SlashCommand[] {
  const out = [...builtins];
  const seen = new Set(builtins.map((c) => c.name));
  for (const c of own) {
    if (seen.has(c.name)) continue;
    seen.add(c.name);
    out.push(c);
  }
  return out;
}

// What to offer for the word being typed ("/", "/sp", "/help"). Names that start
// with it come first, then names that contain it, so "/help" also finds
// "ralph-loop:help". Each group is in name order.
export function matchCommands(list: ReadonlyArray<SlashCommand>, word: string): SlashCommand[] {
  const q = word.replace(/^\//, '').toLowerCase();
  const byName = (a: SlashCommand, b: SlashCommand) => a.name.localeCompare(b.name);
  const starts = list.filter((c) => c.name.toLowerCase().startsWith(q)).sort(byName);
  const contains = q
    ? list
        .filter((c) => !c.name.toLowerCase().startsWith(q) && c.name.toLowerCase().includes(q))
        .sort(byName)
    : [];
  return [...starts, ...contains];
}

// The full list for one agent's composer. The built-ins are there from the first
// render; the agent's own arrive once the sidecar answers. Asked once per pane
// per page, because the folders do not change while someone is typing.
export function useSlashCommands(tmuxSession: string): SlashCommand[] {
  const [own, setOwn] = useState<SlashCommand[]>([]);
  useEffect(() => {
    setOwn([]);
    if (!tmuxSession) return;
    const ac = new AbortController();
    readSessionCommands(tmuxSession, ac.signal)
      .then(setOwn)
      .catch(() => {
        // A failed read leaves the built-ins; there is nothing to tell the
        // operator that would change what they can do.
      });
    return () => ac.abort();
  }, [tmuxSession]);
  return mergeCommands(BUILTIN_COMMANDS, own);
}
