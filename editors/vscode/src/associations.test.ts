import assert from 'node:assert/strict';
import { test } from 'node:test';

import { add, has, outermost, patterns, remove } from './associations.ts';

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

test('outermost', () => {
  assert.deepEqual(outermost([], '/'), []);
  assert.deepEqual(outermost(['/mud/lib/players/me', '/mud', '/mud/lib'], '/'), ['/mud']);
  assert.deepEqual(outermost(['/mud/lib', '/mud/library', '/mud/lib/std', '/mud/lib'], '/').sort(), ['/mud/lib', '/mud/library'],
    'a name that begins like another is not below it');
  assert.deepEqual(outermost(['C:\\mud\\lib\\std', 'C:\\mud\\lib'], '\\'), ['C:\\mud\\lib']);
});
