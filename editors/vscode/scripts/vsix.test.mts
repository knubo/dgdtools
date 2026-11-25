import assert from 'node:assert/strict';
import * as path from 'node:path';
import { test } from 'node:test';

import { bundledDir, bundledPath } from '../src/server.ts';
import { binaries, packageArgs, programName, targets, unpublishable, versionOf, vsixName } from './vsix.mts';
import type { Artifact } from './vsix.mts';

// dist/artifacts.json as GoReleaser 2 writes it for .goreleaser.yaml,
// cut down to what is read and to one entry of each other kind.
const artifacts: Artifact[] = [
  { type: 'Binary', path: 'dist/dgdtools_linux_amd64_v1/dgdtools', goos: 'linux', goarch: 'amd64' },
  { type: 'Binary', path: 'dist/dgdtools_linux_arm64_v8.0/dgdtools', goos: 'linux', goarch: 'arm64' },
  { type: 'Binary', path: 'dist/dgdtools_darwin_amd64_v1/dgdtools', goos: 'darwin', goarch: 'amd64' },
  { type: 'Binary', path: 'dist/dgdtools_darwin_arm64_v8.0/dgdtools', goos: 'darwin', goarch: 'arm64' },
  { type: 'Binary', path: 'dist/dgdtools_windows_amd64_v1/dgdtools.exe', goos: 'windows', goarch: 'amd64' },
  { type: 'Binary', path: 'dist/dgdtools_windows_arm64_v8.0/dgdtools.exe', goos: 'windows', goarch: 'arm64' },
  { type: 'Archive', path: 'dist/dgdtools_0.4.0_linux_amd64.tar.gz', goos: 'linux', goarch: 'amd64' },
  { type: 'Checksum', path: 'dist/checksums.txt' },
];

test('binaries: one for each platform of VS Code that GoReleaser built for', () => {
  const at = (...names: string[]) => path.join('/work/dist', ...names);
  assert.deepEqual(binaries(artifacts, '/work/dist'), [
    { target: 'darwin-arm64', file: at('dgdtools_darwin_arm64_v8.0', 'dgdtools') },
    { target: 'darwin-x64', file: at('dgdtools_darwin_amd64_v1', 'dgdtools') },
    { target: 'linux-arm64', file: at('dgdtools_linux_arm64_v8.0', 'dgdtools') },
    { target: 'linux-x64', file: at('dgdtools_linux_amd64_v1', 'dgdtools') },
    { target: 'win32-arm64', file: at('dgdtools_windows_arm64_v8.0', 'dgdtools.exe') },
    { target: 'win32-x64', file: at('dgdtools_windows_amd64_v1', 'dgdtools.exe') },
  ]);
  assert.equal(new Set(Object.values(targets)).size, Object.keys(targets).length, 'a platform has one binary');
});

test('binaries: what VS Code has no platform for is left out, and a platform is not built twice', () => {
  const more: Artifact[] = [
    { type: 'Binary', path: 'dist/dgdtools_freebsd_amd64_v1/dgdtools', goos: 'freebsd', goarch: 'amd64' },
    { type: 'Binary', path: 'dist/dgdtools_linux_386_sse2/dgdtools', goos: 'linux', goarch: '386' },
    { type: 'Binary', path: 'out\\dgdtools_windows_amd64_v1\\dgdtools.exe', goos: 'windows', goarch: 'amd64' },
  ];
  // The files are looked for in the dist given, whatever it was called
  // and whichever system wrote the list.
  assert.deepEqual(binaries(more, 'elsewhere'), [
    { target: 'win32-x64', file: path.join('elsewhere', 'dgdtools_windows_amd64_v1', 'dgdtools.exe') },
  ]);
  assert.deepEqual(binaries([], 'dist'), []);
  const twice: Artifact[] = [
    { type: 'Binary', path: 'dist/dgdtools_linux_amd64_v1/dgdtools', goos: 'linux', goarch: 'amd64' },
    { type: 'Binary', path: 'dist/dgdtools_linux_amd64_v3/dgdtools', goos: 'linux', goarch: 'amd64' },
  ];
  assert.throws(() => binaries(twice, 'dist'), /two binaries for linux-x64/);
});

test('versionOf: a tag is its version', () => {
  assert.equal(versionOf('v0.4.0'), '0.4.0');
  assert.equal(versionOf('0.4.0'), '0.4.0');
  assert.equal(versionOf('v1.2.3-rc.1'), '1.2.3-rc.1');
  for (const no of ['', 'v1', '1.2', 'main', 'bec1e47', 'v1.2.3.4', '1.2.3 --target x']) {
    assert.throws(() => versionOf(no), /is no version/, no);
  }
});

test('the program is where the client looks for it', () => {
  for (const target of Object.values(targets)) {
    const platform = target.split('-')[0] as NodeJS.Platform;
    const paths = platform === 'win32' ? path.win32 : path.posix;
    assert.equal(paths.join('/ext', bundledDir, programName(target)), bundledPath('/ext', platform), target);
  }
});

test('packageArgs, vsixName', () => {
  assert.equal(vsixName('dgdtools', '0.4.0', 'linux-x64'), 'dgdtools-linux-x64-0.4.0.vsix');
  assert.equal(vsixName('dgdtools', '0.4.0'), 'dgdtools-0.4.0.vsix');
  const common = ['package', '0.4.0', '--no-git-tag-version', '--no-update-package-json', '--no-dependencies', '--out', 'o.vsix'];
  assert.deepEqual(packageArgs({ version: '0.4.0', out: 'o.vsix', target: 'win32-arm64', license: true }), [...common, '--target', 'win32-arm64']);
  assert.deepEqual(packageArgs({ version: '0.4.0', out: 'o.vsix', license: true }), common, 'the package for the rest has no target');
  assert.deepEqual(packageArgs({ version: '0.4.0', out: 'o.vsix', license: false }), [...common, '--skip-license']);
});

test('unpublishable', () => {
  const files = ['package.json', 'README.md', 'LICENSE'];
  assert.deepEqual(unpublishable({ publisher: 'someone' }, files), []);
  assert.deepEqual(unpublishable({ publisher: 'someone', private: false }, ['license.txt']), []);
  assert.match(unpublishable({ publisher: 'someone', private: true }, files).join('\n'), /"private": true/);
  assert.match(unpublishable({}, files).join('\n'), /no "publisher"/);
  assert.match(unpublishable({ publisher: 'someone' }, ['README.md', 'LICENSES']).join('\n'), /no LICENSE/);
  assert.equal(unpublishable({ private: true }, []).length, 3);
});
