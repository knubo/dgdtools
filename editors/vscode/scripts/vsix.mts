// Packages the extension as a release has it: one .vsix for each
// platform, with that platform's dgdtools inside (bin/, where
// src/server.ts looks for it), and one without a program, which is what
// every other platform is given. It is what rust-analyzer and Ruff do.
//
//   node scripts/vsix.mts --dist DIR [--version V] [--out DIR]
//       every package of a release, from what GoReleaser wrote to DIR
//   node scripts/vsix.mts --binary FILE [--target T] [--version V] [--out DIR]
//       one package with that program in it, for this machine or for T
//   node scripts/vsix.mts --publishable
//       says why the extension cannot be published yet, and fails; or nothing
//
// The packing is vsce's, the one package-lock.json pins. V is the
// version the packages say they are (a release's tag, with or without
// its "v"); package.json is not written to. Nothing here publishes.

import { execFileSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

import { bundledDir } from '../src/server.ts';

/**
 * VS Code's name for each platform GoReleaser builds dgdtools for, by
 * GOOS/GOARCH (.goreleaser.yaml; go test ./editors compares the two).
 */
export const targets: Record<string, string> = {
  'darwin/amd64': 'darwin-x64',
  'darwin/arm64': 'darwin-arm64',
  'linux/amd64': 'linux-x64',
  'linux/arm64': 'linux-arm64',
  'windows/amd64': 'win32-x64',
  'windows/arm64': 'win32-arm64',
};

/** An entry of GoReleaser's dist/artifacts.json, as far as it is read. */
export interface Artifact {
  type: string;
  path: string;
  goos?: string;
  goarch?: string;
}

export interface Binary {
  target: string;
  file: string;
}

/**
 * The binaries of a GoReleaser run that VS Code has a platform for,
 * with the files where they are in dist now: a build's directory and
 * the program in it, wherever dist was when it was written.
 */
export function binaries(artifacts: Artifact[], dist: string): Binary[] {
  const found = new Map<string, string>();
  for (const a of artifacts) {
    const target = a.type === 'Binary' ? targets[`${a.goos}/${a.goarch}`] : undefined;
    if (target === undefined) {
      continue;
    }
    if (found.has(target)) {
      throw new Error(`two binaries for ${target}: ${found.get(target)} and ${a.path}`);
    }
    const parts = a.path.split(/[\\/]/);
    found.set(target, path.join(dist, ...parts.slice(-2)));
  }
  return [...found].map(([target, file]) => ({ target, file })).sort((a, b) => a.target.localeCompare(b.target));
}

/** The version of a tag: `v0.4.0` and `0.4.0` are 0.4.0. Nothing else is taken for one. */
export function versionOf(tag: string): string {
  const version = tag.replace(/^v/, '');
  if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
    throw new Error(`"${tag}" is no version: MAJOR.MINOR.PATCH is what an extension has`);
  }
  return version;
}

/** The program's name in a package for target. */
export function programName(target: string): string {
  return target.startsWith('win32-') ? 'dgdtools.exe' : 'dgdtools';
}

/** The package's file name: vsce's own naming. */
export function vsixName(name: string, version: string, target?: string): string {
  return target === undefined ? `${name}-${version}.vsix` : `${name}-${target}-${version}.vsix`;
}

/** The arguments of `vsce package` for one package. */
export function packageArgs(o: { version: string; out: string; target?: string; license: boolean }): string[] {
  // The version is the package's only: package.json keeps its own. The
  // client library is inside dist/extension.js, so there are no
  // dependencies to look for.
  const args = ['package', o.version, '--no-git-tag-version', '--no-update-package-json', '--no-dependencies', '--out', o.out];
  if (o.target !== undefined) {
    args.push('--target', o.target);
  }
  if (!o.license) {
    // vsce stops to ask otherwise. A package without one is for trying
    // out; --publishable refuses it.
    args.push('--skip-license');
  }
  return args;
}

/** Why the extension as it is must not be published; empty when it may be. */
export function unpublishable(manifest: { private?: boolean; publisher?: string }, files: string[]): string[] {
  const why: string[] = [];
  if (manifest.private) {
    why.push('package.json is still "private": true. The owner removes it, and sets "publisher" to the account the extension is published under, when it is to be published.');
  }
  if (!manifest.publisher) {
    why.push('package.json has no "publisher".');
  }
  if (!files.some((f) => /^LICENSE(\.md|\.txt)?$/i.test(f))) {
    why.push('there is no LICENSE in the folder: it is to be the repository\'s, which has none yet.');
  }
  return why;
}

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)));

function hasLicense(): boolean {
  return fs.readdirSync(root).some((f) => /^LICENSE(\.md|\.txt)?$/i.test(f));
}

function main(): void {
  const { values } = parseArgs({
    options: {
      dist: { type: 'string' },
      binary: { type: 'string' },
      target: { type: 'string' },
      version: { type: 'string' },
      out: { type: 'string' },
      publishable: { type: 'boolean' },
    },
  });
  const manifest = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));

  if (values.publishable) {
    const why = unpublishable(manifest, fs.readdirSync(root));
    if (why.length > 0) {
      throw new Error(`the extension is not published:\n${why.map((w) => `  - ${w}`).join('\n')}`);
    }
    return;
  }
  if ((values.dist === undefined) === (values.binary === undefined)) {
    throw new Error('one of --dist DIR (what GoReleaser wrote) and --binary FILE (one program) is needed');
  }

  const version = versionOf(values.version ?? manifest.version);
  const out = path.resolve(values.out ?? root);
  let todo: Binary[];
  if (values.dist !== undefined) {
    const dist = path.resolve(values.dist);
    todo = binaries(JSON.parse(fs.readFileSync(path.join(dist, 'artifacts.json'), 'utf8')), dist);
    if (todo.length === 0) {
      throw new Error(`${dist}/artifacts.json names no binary for a platform of VS Code`);
    }
  } else {
    const target = values.target ?? `${process.platform}-${process.arch}`;
    if (!Object.values(targets).includes(target)) {
      throw new Error(`"${target}" is not a platform dgdtools is released for (${Object.values(targets).join(', ')})`);
    }
    todo = [{ target, file: path.resolve(values.binary!) }];
  }

  const license = hasLicense();
  if (!license) {
    console.warn('vsix: there is no LICENSE in the folder; the packages are made without one, and are not to be published.');
  }
  const vsce = path.join(root, 'node_modules', '@vscode', 'vsce', 'vsce');
  const pack = (target?: string): void => {
    const file = path.join(out, vsixName(manifest.name, version, target));
    // No answers to give: a question vsce asks is a failure.
    execFileSync(process.execPath, [vsce, ...packageArgs({ version, out: file, target, license })], {
      cwd: root, stdio: ['ignore', 'inherit', 'inherit'],
    });
  };
  const bin = path.join(root, bundledDir);
  fs.mkdirSync(out, { recursive: true });
  try {
    for (const { target, file } of todo) {
      fs.rmSync(bin, { recursive: true, force: true });
      fs.mkdirSync(bin);
      const program = path.join(bin, programName(target));
      fs.copyFileSync(file, program);
      // What carried the file here need not have kept the bit.
      fs.chmodSync(program, 0o755);
      pack(target);
    }
  } finally {
    fs.rmSync(bin, { recursive: true, force: true });
  }
  if (values.dist !== undefined) {
    // And the package every other platform is given, which has none.
    pack();
  }
}

if (process.argv[1] !== undefined && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (err) {
    console.error(`vsix: ${err instanceof Error ? err.message : err}`);
    process.exit(1);
  }
}
