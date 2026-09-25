import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  BUILTIN_COMMANDS,
  matchCommands,
  mergeCommands,
  readSessionCommands,
  type SlashCommand,
} from './commands';

const own = (name: string, source: SlashCommand['source'] = 'project'): SlashCommand => ({
  name,
  description: '',
  source,
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('matchCommands', () => {
  const list = mergeCommands(BUILTIN_COMMANDS, [own('ralph-loop:help', 'plugin'), own('helper')]);

  it('puts names that start with the word first, then names that contain it', () => {
    expect(matchCommands(list, '/help').map((c) => c.name)).toEqual([
      'help',
      'helper',
      'ralph-loop:help',
    ]);
  });

  it('offers everything for a bare slash, in name order', () => {
    const names = matchCommands(list, '/').map((c) => c.name);
    expect(names).toHaveLength(list.length);
    expect(names).toEqual([...names].sort((a, b) => a.localeCompare(b)));
  });

  it('ignores case', () => {
    expect(matchCommands([own('Spoon-Feed')], '/spoon').map((c) => c.name)).toEqual(['Spoon-Feed']);
  });
});

describe('mergeCommands', () => {
  it('never lets a skill hide a built-in of the same name', () => {
    const merged = mergeCommands(BUILTIN_COMMANDS, [own('clear'), own('spoon-feed')]);
    expect(merged.filter((c) => c.name === 'clear')).toEqual([
      expect.objectContaining({ source: 'builtin' }),
    ]);
    expect(merged.map((c) => c.name)).toContain('spoon-feed');
  });
});

describe('the built-ins', () => {
  it('leave out the commands that end the agent or touch the shared login', () => {
    const names = BUILTIN_COMMANDS.map((c) => c.name);
    for (const harmful of ['exit', 'quit', 'login', 'logout', 'resume']) {
      expect(names).not.toContain(harmful);
    }
  });
});

describe('readSessionCommands', () => {
  it('keeps well-formed rows and drops the rest', async () => {
    const body = {
      commands: [
        { name: 'spoon-feed', description: 'Surface open decisions', source: 'project' },
        { name: '', description: 'no name', source: 'user' },
        { description: 'missing name' },
        'not a row',
        { name: 'odd-source', description: 7, source: 'somewhere' },
      ],
    };
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status: 200 })),
    );
    expect(await readSessionCommands('agent__x')).toEqual([
      { name: 'spoon-feed', description: 'Surface open decisions', source: 'project' },
      { name: 'odd-source', description: '', source: 'user' },
    ]);
  });

  it('is empty, not an error, when nothing serves /commands', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(new Response('404 page not found\n', { status: 404 })),
    );
    expect(await readSessionCommands('agent__x')).toEqual([]);
  });
});
