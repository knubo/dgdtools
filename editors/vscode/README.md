# DGD LPC (dgdtools)

Support for the [DGD](https://github.com/dworkin/dgd) dialect of LPC in
VS Code, from [dgdtools](https://github.com/mmcdole/dgdtools#readme),
the linter and formatter for it:

- **Syntax highlighting** that knows DGD's own tokens (`({ })` and
  `([ ])` literals, `<-`, `..`, `nil`, `atomic`, `rlimits`, labeled
  inherits), with no server and on a file that does not parse.
- **Diagnostics as you type**: what `dgdtools lint` finds, for the files
  that are open. What the driver will reject when it loads the object
  (syntax errors, constructs DGD does not accept, missing includes) and
  what it will not notice at all (calls to functions and objects that do
  not exist).
- **Fixes** in the lightbulb: a finding's fix when it has one, a comment
  that suppresses it for the line or the file, and *fix all*
  (`source.fixAll.dgdtools`).
- **Formatting** of a file or a selection, as `dgdtools format` writes
  it: one style, and only whitespace changes.
- **Go to definition**: from an `#include` to the header, from an
  `inherit` to the object, from a call to the function (`::fn`,
  `label::fn`, `->fn` on `this_object()` or a path, string callbacks),
  from a macro to its `#define`, from a variable or a parameter to its
  declaration.
- **Hover**: the declaration, the comment above it and where it is, for
  the mudlib's functions, variables and macros and the driver's kfuns.
- **Signature help** in the arguments of a call, while the line is half
  written.
- **Symbols**: the outline of a file, and *Go to Symbol in Workspace*
  for a function, a variable or a header's macro anywhere in the mudlib.
- **Completion**: the locals in scope, the object's variables and
  functions, macros, kfuns and keywords; after `::` and `->` the
  functions that can be called there; `#include` and `inherit` paths;
  the function's name in `call_out("...")`.
- **References** (*Find All References*, *Peek References*): the calls
  of a function across the mudlib, the uses of a variable, a macro or a
  local, the `inherit`s of an object, the `#include`s of a header.
  Calls on an object held in a variable are not listed; the output
  channel *dgdtools* says how many there were, and in a folder with a
  profile that the search covered your folder only.
- **Highlights** of the name under the cursor, a variable marked where
  it is read and where it is written.
- **Rename** (*Rename Symbol*, F2) as `dgdtools rename` makes it: in
  the file (a local, a parameter, a private function or variable, an
  inherit label, a macro of the file) or across the mudlib (a function
  with its redefinitions up and down the inherit hierarchy and every call
  that reaches one of them, a global variable, a header's macro). The
  preview (*Shift+Enter*, or when something needs confirming) shows each
  file's edits; the places that may be the name and nobody can tell (a
  call on an object held in a variable, the name in a macro's body) are
  there unticked, grouped by why. Where it cannot be renamed, or not to
  the name typed (the name would hide another or be hidden, a variable
  `save_object` writes, the create function), the editor says why before
  anything changes; a message says what else to know (edited files other
  objects inherit, which take effect at the scheduled reboot).
- **Files moved**: rename or move a `.c` or `.h` file or a folder in the
  explorer, and the `inherit` and `#include` lines and object paths that
  name it are rewritten with it; a path made by a macro is left, and a
  message says where.
- **Refactorings** in the light bulb: the path of the one file a missing
  `#include` or `inherit` may have meant, and "Add the prototype" at a
  call of a function the file defines later.
- **Semantic highlighting** over the grammar's: macros (one that stands
  for a type as a type), functions where declared and defined, and in a
  call the object's own, inherited, the auto object's or the driver's,
  `->fn` as a method, global and local variables and parameters,
  inherit labels, and `function` where the mudlib has closures. A theme
  with no rules for semantic tokens colours them by the grammar's
  scopes (`support.function` for a kfun, `storage.type` for a type
  macro); the modifiers `inherited`, `local` and `macro` can be named in
  `editor.semanticTokenColorCustomizations`.

Where the tools cannot know (a call on an object held in a variable)
they say nothing rather than guess.

The language is called `dgdlpc`, *LPC (DGD)* in the language picker. The
editor shows what the command line prints: both read the mudlib's
`dgdtools.yml`.

## Installing

The extension comes as one package for each platform, with that
platform's `dgdtools` inside: on macOS, Linux and Windows (x64 and arm64
each) there is nothing else to install. On any other platform the
package is the extension without the program:
[install dgdtools](https://github.com/mmcdole/dgdtools#installation)
yourself, on your `PATH`.

The packages are also files of each
[release of dgdtools](https://github.com/mmcdole/dgdtools/releases),
`dgdtools-<platform>-<version>.vsix` and, without the program,
`dgdtools-<version>.vsix`. One is installed with *Extensions: Install
from VSIX…* in the command palette, or

```
code --install-extension dgdtools-darwin-arm64-0.4.0.vsix
```

## Saying which files are LPC

The extension claims no file by its name: a `.c` file is C on your
machine everywhere but in a mudlib. Open the mudlib's folder and run
**dgdtools: Use DGD LPC for This Folder's .c/.h Files** (the command palette, or a
folder's menu in the explorer); the extension offers it by itself when
the folder has a `dgdtools.yml`. It writes `files.associations` to the
workspace's settings and never to your own:

```json
{
  "files.associations": { "*.c": "dgdlpc", "*.h": "dgdlpc" }
}
```

For a folder below the one that is open, or one folder of a workspace
with several, the patterns name the folder (`**/lib/**/*.c` for a
folder `lib`). A single file can be switched with *Change Language
Mode* → *LPC (DGD)*.

It also adds Microsoft's C extensions to the folder's
`.vscode/extensions.json` as `unwantedRecommendations`, so that VS Code
does not recommend them for its `.c` files. The inverse command takes
out what the first put in and nothing else, and deletes a file it leaves
empty.

Word-based suggestions are off for `dgdlpc` by default
(`editor.wordBasedSuggestions`): the server's completion knows the
names.

A mudlib whose files are Latin-1 should say that too, or saving a file
opened as UTF-8 damages it:

```json
{
  "[dgdlpc]": { "files.encoding": "iso88591" }
}
```

To fix and format on saving:

```json
{
  "[dgdlpc]": {
    "editor.formatOnSave": true,
    "editor.codeActionsOnSave": { "source.fixAll.dgdtools": "explicit" }
  }
}
```

What the grammar cannot know, being one file for every mudlib: `//`
is always a comment, whatever `dialect.slash_slash` says (where the
driver has no `//` comments the linter reports them), and `function`
is coloured as a type only through the server, where the mudlib sets
`dialect.closures`.

## Commands

| command | |
|---|---|
| **dgdtools: Use DGD LPC for This Folder's .c/.h Files** | the `*.c` and `*.h` files of a folder are `dgdlpc`, in the workspace's settings |
| **dgdtools: Use C for This Folder's .c/.h Files** | takes out what the first command put in |
| **dgdtools: Restart Server** | stops the language server and starts it again |

## Settings

| setting | |
|---|---|
| `dgdtools.path` | the `dgdtools` executable to run: an absolute path (`~` is your home directory) or a command name on `PATH`. Empty: the one this extension came with, else `dgdtools` on `PATH` |
| `dgdtools.profile` | the mudlib profile to work against in a folder that has no `dgdtools.yml`: a name or a file, as `profile:` in a config file |
| `dgdtools.libPath` | where a folder that has no `dgdtools.yml` sits in the mudlib (for example `/players/me`), as `lib_path:` in a config file |
| `dgdtools.logFile` | a file the server appends its debug log to |
| `dgdtools.trace.server` | `messages` or `verbose`: how much is traced while the log level of the *dgdtools* channel of the Output panel is *Trace* |

A `dgdtools.yml` always wins over `dgdtools.profile` and
`dgdtools.libPath`, and everything else about the tools is the mudlib's to
say, in that file: an editor cannot make the server disagree with the
command line.

## Which dgdtools runs

The program `dgdtools.path` names, when it is set. Else the one in the
extension's package, which is the one of the extension's own release.
Else `dgdtools` on `PATH`. To have the editor run what your shell runs,
set `dgdtools.path` to `dgdtools`. The *dgdtools* channel of the Output
panel says which program was started, and its version. In a workspace
that is not trusted, what the workspace itself says of `dgdtools.path`
and `dgdtools.logFile` is not listened to. A change of either restarts
the server, as **dgdtools: Restart Server** does.

A declaration that comes from a mudlib profile opens read-only, as a
`dgdtools-profile:` document whose text the server gives.

## More

- [dgdtools](https://github.com/mmcdole/dgdtools#readme): the rules, the
  formatter, `dgdtools.yml`, mudlib profiles.
- [Editor support](https://github.com/mmcdole/dgdtools/blob/main/editors/README.md):
  what the grammar and the server do in detail, Vim and Neovim, other
  editors, and how to build this extension from a checkout.
