import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { test } from 'node:test';

import { expandHome, explain, findOnPath, resolveServer, serverArgs } from './server.ts';
import type { Host, Missing } from './server.ts';

const posix = process.platform !== 'win32';

// A directory tree on disk: a name that ends in "/" is a directory, a
// name that begins with "+" is a file that can be run.
function tree(t: { after(fn: () => void): void }, names: string[]): string {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'dgdtools-vscode-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  for (const name of names) {
    const runs = path.basename(name).startsWith('+');
    const file = path.join(root, path.dirname(name), path.basename(name).replace(/^\+/, ''));
    if (name.endsWith('/')) {
      fs.mkdirSync(file, { recursive: true });
      continue;
    }
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, '');
    fs.chmodSync(file, runs ? 0o755 : 0o644);
  }
  return root;
}

function host(env: Record<string, string>, home = '/home/me'): Host {
  return { platform: process.platform, env, home };
}

test('expandHome', () => {
  assert.equal(expandHome('~', '/home/me'), '/home/me');
  assert.equal(expandHome('~/go/bin/dgdtools', '/home/me'), path.join('/home/me', 'go/bin/dgdtools'));
  assert.equal(expandHome('/usr/bin/dgdtools', '/home/me'), '/usr/bin/dgdtools');
  assert.equal(expandHome('~other/dgdtools', '/home/me'), '~other/dgdtools', 'another user is not this one');
  assert.equal(expandHome('', '/home/me'), '');
});

test('findOnPath takes the first directory of PATH that has the program', { skip: !posix }, (t) => {
  const root = tree(t, ['a/', 'b/+dgdtools', 'c/+dgdtools', 'c/+other']);
  const PATH = ['a', 'b', 'c'].map((d) => path.join(root, d)).join(path.delimiter);
  assert.equal(findOnPath('dgdtools', host({ PATH })), path.join(root, 'b', 'dgdtools'));
  assert.equal(findOnPath('other', host({ PATH })), path.join(root, 'c', 'other'));
  assert.equal(findOnPath('none', host({ PATH })), undefined);
  assert.equal(findOnPath('dgdtools', host({})), undefined, 'no PATH at all');
});

test('findOnPath passes over what cannot be run', { skip: !posix }, (t) => {
  const root = tree(t, ['plain/dgdtools', 'dir/dgdtools/', 'good/+dgdtools']);
  const PATH = ['missing', 'plain', 'dir', '', 'good'].map((d) => (d === '' ? '' : path.join(root, d))).join(path.delimiter);
  assert.equal(findOnPath('dgdtools', host({ PATH })), path.join(root, 'good', 'dgdtools'));
  // An empty entry of PATH is the current directory to a shell. It is
  // not looked in: the directory VS Code runs in is not the user's.
  const cwd = process.cwd();
  process.chdir(path.join(root, 'good'));
  t.after(() => process.chdir(cwd));
  assert.equal(findOnPath('dgdtools', host({ PATH: path.delimiter })), undefined);
});

test('findOnPath on Windows looks for NAME.exe', () => {
  // The disk there has no case; the variable's name has none either.
  const have = new Set(['c:\\go\\bin\\dgdtools.cmd', 'c:\\tools\\dgdtools.exe', 'c:\\tools\\lint.exe']);
  const win = (env: Record<string, string>): Host => ({
    platform: 'win32', env, home: 'C:\\Users\\me', isExecutable: (p) => have.has(p.toLowerCase()),
  });
  const dirs = 'C:\\empty;C:\\go\\bin;;C:\\tools';
  assert.equal(findOnPath('dgdtools', win({ Path: dirs })), 'C:\\tools\\dgdtools.exe', 'a .cmd cannot be started without a shell');
  assert.equal(findOnPath('dgdtools.EXE', win({ PATH: dirs })), 'C:\\tools\\dgdtools.EXE', 'a name that has its extension');
  assert.equal(findOnPath('lint', win({ PATH: dirs })), 'C:\\tools\\lint.exe');
  assert.equal(findOnPath('dgdtools', win({ PATH: 'C:\\go\\bin' })), undefined);
  assert.equal(findOnPath('dgdtools', win({})), undefined);
  assert.deepEqual(resolveServer('', win({ Path: dirs })), { path: 'C:\\tools\\dgdtools.exe' });
  assert.deepEqual(resolveServer('C:\\tools\\lint.exe', win({})), { path: 'C:\\tools\\lint.exe' });
  assert.deepEqual(resolveServer('tools\\lint.exe', win({})), { problem: 'not-absolute', path: 'tools\\lint.exe' });
  assert.deepEqual(resolveServer('~\\bin\\dgdtools.exe', win({})), { problem: 'not-a-program', path: 'C:\\Users\\me\\bin\\dgdtools.exe' });
});

test('resolveServer', { skip: !posix }, (t) => {
  const root = tree(t, ['bin/+dgdtools', 'bin/+dgdtools-dev', 'home/go/bin/+dgdtools', 'home/notes.txt']);
  const h: Host = { platform: process.platform, env: { PATH: path.join(root, 'bin') }, home: path.join(root, 'home') };
  const onPath = { path: path.join(root, 'bin', 'dgdtools') };
  assert.deepEqual(resolveServer('', h), onPath, 'empty: dgdtools on PATH');
  assert.deepEqual(resolveServer('  ', h), onPath);
  assert.deepEqual(resolveServer('dgdtools-dev', h), { path: path.join(root, 'bin', 'dgdtools-dev') }, 'a bare name is a command');
  assert.deepEqual(resolveServer('~/go/bin/dgdtools', h), { path: path.join(root, 'home', 'go', 'bin', 'dgdtools') });
  assert.deepEqual(resolveServer(path.join(root, 'bin', 'dgdtools'), h), onPath);

  assert.deepEqual(resolveServer('', { ...h, env: {} }), { problem: 'not-on-path', command: 'dgdtools' });
  assert.deepEqual(resolveServer('lpc', h), { problem: 'not-on-path', command: 'lpc' });
  assert.deepEqual(resolveServer('~/notes.txt', h), { problem: 'not-a-program', path: path.join(root, 'home', 'notes.txt') });
  assert.deepEqual(resolveServer('~/go', h), { problem: 'not-a-program', path: path.join(root, 'home', 'go') }, 'a directory');
  assert.deepEqual(resolveServer('/nowhere/dgdtools', h), { problem: 'not-a-program', path: '/nowhere/dgdtools' });
  assert.deepEqual(resolveServer('bin/dgdtools', h), { problem: 'not-absolute', path: 'bin/dgdtools' });
  assert.deepEqual(resolveServer('./dgdtools', h), { problem: 'not-absolute', path: './dgdtools' });
});

test('explain says what was looked for and what to do', () => {
  const cases: Missing[] = [
    { problem: 'not-on-path', command: 'dgdtools' },
    { problem: 'not-on-path', command: 'lpc' },
    { problem: 'not-a-program', path: '/opt/x/dgdtools' },
    { problem: 'not-absolute', path: 'bin/dgdtools' },
  ];
  for (const c of cases) {
    const msg = explain(c);
    assert.match(msg, /dgdtools\.path/, msg);
    assert.match(msg, /was not started/, msg);
    assert.ok(msg.includes('command' in c ? c.command : c.path), msg);
  }
  assert.match(explain(cases[0]!), /PATH/);
  assert.match(explain(cases[0]!), /Restart Server/);
});

test('serverArgs', () => {
  assert.deepEqual(serverArgs('', '/home/me'), ['lsp']);
  assert.deepEqual(serverArgs('  ', '/home/me'), ['lsp']);
  assert.deepEqual(serverArgs('/tmp/lsp.log', '/home/me'), ['lsp', '--log', '/tmp/lsp.log']);
  assert.deepEqual(serverArgs('~/lsp.log', '/home/me'), ['lsp', '--log', path.join('/home/me', 'lsp.log')]);
});
