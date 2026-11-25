// What "this folder is DGD LPC" is in VS Code: entries of
// `files.associations` in the workspace's settings that make the
// folder's *.c and *.h files `dgdlpc`. Nothing here knows VS Code:
// `npm test` runs it under plain Node.

import { applyEdits, modify, parse, stripComments } from 'jsonc-parser';

/** The language id, as the manifest contributes it. */
export const language = 'dgdlpc';

/** The value of `files.associations`: a glob pattern to a language id. */
export type Associations = Record<string, string>;

// The patterns that stand for the *.c and *.h files of one folder.
//
// `files.associations` is a setting of the window, not of a folder, so a
// pattern has to say which folder it means. `segments` are the names
// from the workspace folder down to the folder (none for the workspace
// folder itself), and `alone` says the workspace is that one folder and
// its settings are the folder's own `.vscode/settings.json`.
//
// For such a workspace's own folder the patterns are `*.c` and `*.h`,
// which VS Code matches against a file's name. Anywhere else they name
// the folder, and VS Code matches a pattern with a `/` against the whole
// path of a file: `**/lib/**/*.c` for the folder `lib`, relative so that
// the settings mean the same in another checkout. `name` is the
// workspace folder's own name, which leads the pattern when the
// workspace has other folders beside it.
export function patterns(segments: readonly string[], name: string, alone: boolean): string[] {
  const dirs = alone ? [...segments] : [name, ...segments];
  const prefix = dirs.length === 0 ? '' : `**/${dirs.join('/')}/**/`;
  return [`${prefix}*.c`, `${prefix}*.h`];
}

/** Whether every one of the patterns is `dgdlpc` already. */
export function has(current: Associations | undefined, pats: readonly string[]): boolean {
  return pats.every((p) => current?.[p] === language);
}

/** `current` with the patterns as `dgdlpc`; everything else as it was. */
export function add(current: Associations | undefined, pats: readonly string[]): Associations {
  const next: Associations = { ...current };
  for (const p of pats) {
    next[p] = language;
  }
  return next;
}

/**
 * `current` without the patterns that are `dgdlpc`: one the user gave
 * another language is theirs. Undefined when nothing is left, which
 * takes the setting out of the file.
 */
export function remove(current: Associations | undefined, pats: readonly string[]): Associations | undefined {
  const next: Associations = { ...current };
  for (const p of pats) {
    if (next[p] === language) {
      delete next[p];
    }
  }
  return Object.keys(next).length === 0 ? undefined : next;
}

/**
 * Of some directories, those that lie under none of the others: the
 * outermost of each nest. Paths are compared as given, by segment.
 */
export function outermost(dirs: readonly string[], sep: string): string[] {
  const sorted = [...new Set(dirs)].sort((a, b) => a.length - b.length);
  const kept: string[] = [];
  for (const dir of sorted) {
    if (!kept.some((k) => dir.startsWith(k.endsWith(sep) ? k : k + sep))) {
      kept.push(dir);
    }
  }
  return kept;
}

// A folder of DGD LPC is a folder of *.c files Microsoft's C extensions
// would take for C: the workspace's .vscode/extensions.json says they
// are not wanted there, so VS Code does not recommend them.

/** The extensions a folder of DGD LPC does not want recommended. */
export const unwanted = ['ms-vscode.cpptools', 'ms-vscode.cpptools-extension-pack'];

const key = 'unwantedRecommendations';
const style = { formattingOptions: { insertSpaces: true, tabSize: 2, eol: '\n' } };

function list(text: string): unknown[] {
  const value = (parse(text) as Record<string, unknown> | undefined)?.[key];
  return Array.isArray(value) ? value : [];
}

/**
 * The text of an extensions.json (undefined: there is none) with the C
 * extensions among its unwanted recommendations, everything else as it
 * was, and those of them it added.
 */
export function addUnwanted(text: string | undefined): { text: string; added: string[] } {
  let out = text?.trim() ? text : '{}\n';
  const added = unwanted.filter((id) => !list(out).includes(id));
  for (const id of added) {
    const path = Array.isArray((parse(out) as Record<string, unknown> | undefined)?.[key]) ? [key, -1] : [key];
    const value = path.length === 1 ? [id] : id;
    out = applyEdits(out, modify(out, path, value, { ...style, isArrayInsertion: path.length === 2 }));
  }
  return { text: out, added };
}

/**
 * The text of an extensions.json without the unwanted recommendations
 * `ours` (what addUnwanted added), the list going when it is left empty
 * and the file has no comment (which might be about it); undefined when
 * nothing at all is left in the file, which then goes.
 */
export function removeUnwanted(text: string, ours: readonly string[]): string | undefined {
  let out = text;
  for (const id of ours) {
    const at = list(out).indexOf(id);
    if (at >= 0) {
      out = applyEdits(out, modify(out, [key, at], undefined, style));
    }
  }
  if (list(out).length === 0 && Array.isArray((parse(out) as Record<string, unknown> | undefined)?.[key]) && stripComments(out) === out) {
    out = applyEdits(out, modify(out, [key], undefined, style));
  }
  return empty(out) ? undefined : out;
}

/**
 * Whether the text of a settings file is an empty object and nothing
 * else (no comment either): what VS Code leaves when the last setting
 * of a file is taken out.
 */
export function empty(text: string): boolean {
  return /^\s*\{\s*\}\s*$/.test(text);
}
