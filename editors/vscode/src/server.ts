// Which program the language server is, and how it is started. Nothing
// here knows VS Code: `npm test` runs it under plain Node.

import * as fs from 'node:fs';
import * as path from 'node:path';

/** The command looked for on PATH when `dgdtools.path` is empty. */
export const defaultCommand = 'dgdtools';

/** Where the installation instructions are. */
export const installURL = 'https://github.com/mmcdole/dgdtools#installation';

/** What a lookup needs to know of the machine. */
export interface Host {
  platform: NodeJS.Platform;
  /** The environment, for its PATH. */
  env: Record<string, string | undefined>;
  home: string;
  /** Whether p is a file that can be run; the disk's answer when left out. */
  isExecutable?: (p: string) => boolean;
}

export type Found = { path: string };
/** Why there is no server to start. */
export type Missing =
  | { problem: 'not-on-path'; command: string }
  | { problem: 'not-a-program'; path: string }
  | { problem: 'not-absolute'; path: string };

/** `~` and `~/x` are the home directory, as in a shell. */
export function expandHome(p: string, home: string, platform: NodeJS.Platform = process.platform): string {
  if (p === '~') {
    return home;
  }
  const paths = platform === 'win32' ? path.win32 : path.posix;
  return p.startsWith('~/') || p.startsWith('~\\') ? paths.join(home, p.slice(2)) : p;
}

function isExecutable(p: string, platform: NodeJS.Platform): boolean {
  try {
    if (!fs.statSync(p).isFile()) {
      return false;
    }
    // Windows has no bit to ask about: the extension says it.
    if (platform !== 'win32') {
      fs.accessSync(p, fs.constants.X_OK);
    }
    return true;
  } catch {
    return false;
  }
}

/**
 * The file a command name stands for, as a shell finds it: in the first
 * directory of PATH that has it. On Windows the file is NAME.exe: that
 * is what a process can be started from without a shell, and what
 * dgdtools is there.
 */
export function findOnPath(command: string, host: Host): string | undefined {
  const win = host.platform === 'win32';
  const paths = win ? path.win32 : path.posix;
  const runs = host.isExecutable ?? ((p: string) => isExecutable(p, host.platform));
  let dirs = host.env['PATH'];
  if (win) {
    // The variable is "Path" there, where names have no case.
    dirs ??= Object.entries(host.env).find(([name]) => name.toUpperCase() === 'PATH')?.[1];
    if (!command.toLowerCase().endsWith('.exe')) {
      command += '.exe';
    }
  }
  for (const dir of (dirs ?? '').split(paths.delimiter)) {
    // An empty entry is the current directory to a shell, and VS Code's
    // is not the user's.
    if (dir !== '' && runs(paths.join(dir, command))) {
      return paths.join(dir, command);
    }
  }
  return undefined;
}

/**
 * The server to start for a value of `dgdtools.path`: empty is
 * `dgdtools` on PATH, a bare name is that command on PATH, and anything
 * else is the file itself, which must be absolute once `~` is the home
 * directory.
 */
export function resolveServer(setting: string, host: Host): Found | Missing {
  const value = setting.trim();
  if (value === '' || !/[\\/]/.test(value)) {
    const command = value || defaultCommand;
    const found = findOnPath(command, host);
    return found ? { path: found } : { problem: 'not-on-path', command };
  }
  const file = expandHome(value, host.home, host.platform);
  const paths = host.platform === 'win32' ? path.win32 : path.posix;
  if (!paths.isAbsolute(file)) {
    return { problem: 'not-absolute', path: value };
  }
  const runs = host.isExecutable ?? ((p: string) => isExecutable(p, host.platform));
  return runs(file) ? { path: file } : { problem: 'not-a-program', path: file };
}

/** What to tell the user, and what they can do about it. */
export function explain(missing: Missing): string {
  const install =
    'Install dgdtools (see "How to Install"), or set dgdtools.path to where it is; then run "dgdtools: Restart Server".';
  switch (missing.problem) {
    case 'not-on-path':
      return missing.command === defaultCommand
        ? `dgdtools: the language server was not started, because there is no "dgdtools" on PATH. ${install}`
        : `dgdtools: the language server was not started, because dgdtools.path names "${missing.command}" and there is no such command on PATH. ${install}`;
    case 'not-a-program':
      return `dgdtools: the language server was not started, because dgdtools.path is "${missing.path}", which is not a program that can be run. Set it to the dgdtools executable, or clear it to use the one on PATH.`;
    case 'not-absolute':
      return `dgdtools: the language server was not started, because dgdtools.path is "${missing.path}", a relative path. Give the whole path of the dgdtools executable, or a command name to look up on PATH.`;
  }
}

/**
 * The arguments of the server's command. The client adds `--stdio`
 * itself, which the server takes.
 */
export function serverArgs(logFile: string, home: string): string[] {
  const args = ['lsp'];
  const log = expandHome(logFile.trim(), home);
  if (log !== '') {
    args.push('--log', log);
  }
  return args;
}
