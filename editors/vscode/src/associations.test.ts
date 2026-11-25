import assert from 'node:assert/strict';
import { test } from 'node:test';

import { parse } from 'jsonc-parser';

import { add, addUnwanted, anyIn, empty, has, outermost, patterns, remove, removeUnwanted, unwanted } from './associations.ts';

test('patterns', () => {
  // The one folder of a workspace: its settings are its own, and a
  // pattern without "/" is matched against a file's name.
  assert.deepEqual(patterns([], 'mud', true), ['*.c', '*.h']);
  // A folder below it is named, relative to wherever the checkout is.
  assert.deepEqual(patterns(['lib'], 'mud', true), ['**/lib/**/*.c', '**/lib/**/*.h']);
  assert.deepEqual(patterns(['lib', 'players', 'me'], 'mud', true), ['**/lib/players/me/**/*.c', '**/lib/players/me/**/*.h']);
  // In a workspace of several folders the settings are the workspace's,
  // and "*.c" would be every folder's.
  assert.deepEqual(patterns([], 'mud', false), ['**/mud/**/*.c', '**/mud/**/*.h']);
  assert.deepEqual(patterns(['lib'], 'mud', false), ['**/mud/lib/**/*.c', '**/mud/lib/**/*.h']);
});

test('add keeps what is there', () => {
  const pats = patterns([], 'mud', true);
  assert.deepEqual(add(undefined, pats), { '*.c': 'dgdlpc', '*.h': 'dgdlpc' });
  const current = { '*.tpl': 'html', '*.h': 'cpp' };
  assert.deepEqual(add(current, pats), { '*.tpl': 'html', '*.h': 'dgdlpc', '*.c': 'dgdlpc' });
  assert.deepEqual(current, { '*.tpl': 'html', '*.h': 'cpp' }, 'the value read from the settings is not written to');
});

test('has', () => {
  const pats = patterns(['lib'], 'mud', true);
  assert.equal(has(undefined, pats), false);
  assert.equal(has({ '**/lib/**/*.c': 'dgdlpc' }, pats), false, 'one of the two');
  assert.equal(has({ '**/lib/**/*.c': 'dgdlpc', '**/lib/**/*.h': 'c' }, pats), false);
  assert.equal(has(add({ '*.c': 'c' }, pats), pats), true);
});

test('remove takes out what add put in, and nothing else', () => {
  const pats = patterns([], 'mud', true);
  assert.equal(remove(add(undefined, pats), pats), undefined, 'nothing left: the setting goes');
  assert.equal(remove(undefined, pats), undefined);
  assert.deepEqual(remove({ '*.tpl': 'html', '*.c': 'dgdlpc', '*.h': 'cpp' }, pats), { '*.tpl': 'html', '*.h': 'cpp' },
    'a pattern given another language is the user\'s');
  const other = patterns(['lib'], 'mud', true);
  const both = add(add(undefined, pats), other);
  assert.deepEqual(remove(both, other), { '*.c': 'dgdlpc', '*.h': 'dgdlpc' });
});

test('anyIn: what is left of one workspace folder', () => {
  // The workspace is one folder: every pattern is its own.
  assert.equal(anyIn(add(undefined, patterns(['lib'], 'mud', true)), 'mud', true), true);
  assert.equal(anyIn({ '*.c': 'c' }, 'mud', true), false);
  assert.equal(anyIn(undefined, 'mud', true), false);
  // Several folders: B's patterns say nothing of A, nor of a folder
  // whose name begins like A's.
  const both = add(add(undefined, patterns([], 'a', false)), patterns(['lib'], 'b', false));
  const left = remove(both, patterns([], 'a', false));
  assert.equal(anyIn(left, 'a', false), false);
  assert.equal(anyIn(left, 'b', false), true);
  assert.equal(anyIn(add(undefined, patterns([], 'ab', false)), 'a', false), false);
  assert.equal(anyIn(add(undefined, patterns(['lib'], 'a', false)), 'a', false), true);
});

test('outermost', () => {
  assert.deepEqual(outermost([], '/'), []);
  assert.deepEqual(outermost(['/mud/lib/players/me', '/mud', '/mud/lib'], '/'), ['/mud']);
  assert.deepEqual(outermost(['/mud/lib', '/mud/library', '/mud/lib/std', '/mud/lib'], '/').sort(), ['/mud/lib', '/mud/library'],
    'a name that begins like another is not below it');
  assert.deepEqual(outermost(['C:\\mud\\lib\\std', 'C:\\mud\\lib'], '\\'), ['C:\\mud\\lib']);
});

test('the C extensions are unwanted, and only what was added goes', () => {
  // No file: one with the two.
  const made = addUnwanted(undefined);
  assert.deepEqual(made.added, unwanted);
  assert.deepEqual(JSON.parse(made.text), { unwantedRecommendations: unwanted });
  assert.equal(removeUnwanted(made.text, made.added), undefined, 'nothing left: the file goes');

  // A file of the user's, with a comment: merged into, and given back.
  const theirs = '{\n  // ours\n  "recommendations": ["mmcdole.dgdtools"],\n  "unwantedRecommendations": ["ms-vscode.cpptools"]\n}\n';
  const merged = addUnwanted(theirs);
  assert.deepEqual(merged.added, ['ms-vscode.cpptools-extension-pack'], 'one was there already');
  assert.match(merged.text, /\/\/ ours/);
  assert.match(merged.text, /"ms-vscode.cpptools",\s*"ms-vscode.cpptools-extension-pack"/);
  const back = removeUnwanted(merged.text, merged.added) ?? '';
  assert.match(back, /\/\/ ours/);
  assert.deepEqual(parse(back), parse(theirs), 'what the user had, the list laid out again');
  assert.deepEqual(addUnwanted(merged.text).added, [], 'nothing to add twice');

  // A file with a comment is kept, and so is its list, emptied.
  assert.equal(removeUnwanted('{\n  // keep\n  "unwantedRecommendations": ["ms-vscode.cpptools"]\n}\n', unwanted),
    '{\n  // keep\n  "unwantedRecommendations": []\n}\n');
  assert.equal(removeUnwanted('{"recommendations": []}', unwanted), '{"recommendations": []}');
  assert.equal(addUnwanted('').text, made.text, 'an empty file is no file');
});

test('empty', () => {
  assert.equal(empty('{}'), true);
  assert.equal(empty('{\n}\n'), true);
  assert.equal(empty('{\n  // a comment\n}\n'), false);
  assert.equal(empty('{"a": 1}'), false);
});
