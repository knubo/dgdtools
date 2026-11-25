// The VS Code side of dgdtools: it starts `dgdtools lsp` and lets
// vscode-languageclient do the talking, and it has the command that says
// which folder's *.c and *.h files are LPC. What can be decided without
// VS Code is in server.ts and associations.ts, where it is tested.

import * as os from 'node:os';
import * as path from 'node:path';
import * as vscode from 'vscode';
import { LanguageClient, TransportKind } from 'vscode-languageclient/node';
import type { LanguageClientOptions, ServerOptions } from 'vscode-languageclient/node';

import * as associations from './associations.ts';
import { explain, installURL, makeRunnable, resolveServer, serverArgs } from './server.ts';

// The scheme of the documents that are a profile's stubs (profile.Scheme
// on the server's side). Their text is the server's to give: it says so
// in its capabilities (workspace.textDocumentContent), and the client
// library registers the content provider that asks it. A stub is
// therefore there to read, and cannot be edited, while the server runs.
const stubScheme = 'dgdtools-profile';

let output: vscode.LogOutputChannel;
// The folder the extension is installed in: a package made for this
// platform has dgdtools in it.
let extensionPath: string;
let client: LanguageClient | undefined;
// Starts and stops happen one after the other, in the order asked for.
let turn: Promise<void> = Promise.resolve();

export function activate(context: vscode.ExtensionContext): void {
  output = vscode.window.createOutputChannel('dgdtools', { log: true });
  extensionPath = context.extensionPath;
  context.subscriptions.push(
    output,
    vscode.commands.registerCommand('dgdtools.restartServer', restart),
    vscode.commands.registerCommand('dgdtools.associateFolder', (uri?: vscode.Uri) => setAssociation(uri, true)),
    vscode.commands.registerCommand('dgdtools.dissociateFolder', (uri?: vscode.Uri) => setAssociation(uri, false)),
    // The two settings that are the server's command line. The others
    // the server asks for itself, and is told when they change.
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('dgdtools.path') || e.affectsConfiguration('dgdtools.logFile')) {
        void restart();
      }
    }),
    vscode.workspace.onDidOpenTextDocument(stubIsLPC),
  );
  // The folder commands are in the explorer's menu where the extension
  // is at work, and nowhere else.
  void vscode.commands.executeCommand('setContext', 'dgdtools.active', true);
  void restart();
  void offerAssociations(context);
}

export function deactivate(): Promise<void> {
  turn = turn.then(stop);
  return turn;
}

function restart(): Promise<void> {
  turn = turn
    .then(stop)
    .then(start)
    .catch((err) => output.error(`restarting the server: ${err}`));
  return turn;
}

async function start(): Promise<void> {
  const config = vscode.workspace.getConfiguration('dgdtools');
  const home = os.homedir();
  const server = resolveServer(config.get<string>('path', ''), {
    platform: process.platform, env: process.env, home, extensionPath,
  });
  if ('problem' in server) {
    output.error(explain(server));
    void reportMissing(explain(server));
    return;
  }
  const args = serverArgs(config.get<string>('logFile', ''), home);
  const serverOptions: ServerOptions = { command: server.path, args, transport: TransportKind.stdio };
  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ language: associations.language }],
    // The server asks for the section, for each folder; this has it told
    // when the section changes, so that it asks again.
    synchronize: { configurationSection: 'dgdtools' },
    outputChannel: output,
  };
  if (server.source === 'bundled') {
    makeRunnable(server.path);
  }
  const whence = { setting: 'dgdtools.path', bundled: 'the one this extension came with', PATH: 'found on PATH' };
  output.info(`starting ${server.path} ${args.join(' ')} (${whence[server.source]})`);
  client = new LanguageClient('dgdtools', 'dgdtools', serverOptions, clientOptions);
  try {
    await client.start();
    output.info(`the server is dgdtools ${client.initializeResult?.serverInfo?.version ?? '(it does not say which version)'}`);
  } catch (err) {
    // The client has told the user already.
    output.error(`the server did not start: ${err}`);
  }
}

async function stop(): Promise<void> {
  const old = client;
  client = undefined;
  try {
    await old?.dispose();
  } catch (err) {
    // A client that never got to run cannot be stopped, and says so.
    output.debug(`stopping the server: ${err}`);
  }
}

async function reportMissing(message: string): Promise<void> {
  const settings = 'Open Settings';
  const install = 'How to Install';
  const choice = await vscode.window.showErrorMessage(message, settings, install);
  if (choice === settings) {
    await vscode.commands.executeCommand('workbench.action.openSettings', 'dgdtools.path');
  } else if (choice === install) {
    await vscode.env.openExternal(vscode.Uri.parse(installURL));
  }
}

// A stub is LPC whatever its name is associated with: the workspace's
// associations are about its own folders, and a stub lies in none.
function stubIsLPC(doc: vscode.TextDocument): void {
  if (doc.uri.scheme === stubScheme && doc.languageId !== associations.language) {
    void vscode.languages.setTextDocumentLanguage(doc, associations.language);
  }
}

// A folder, as the association commands see it: the patterns that stand
// for its files, and what to call it.
interface Place {
  patterns: string[];
  label: string;
}

function place(dir: vscode.Uri): Place | undefined {
  const folder = vscode.workspace.getWorkspaceFolder(dir);
  if (!folder || dir.scheme !== 'file') {
    return undefined;
  }
  const rel = path.relative(folder.uri.fsPath, dir.fsPath);
  const segments = rel === '' ? [] : rel.split(path.sep);
  // Without a workspace file the workspace is this one folder, and its
  // settings are the folder's own.
  const alone = vscode.workspace.workspaceFile === undefined;
  return {
    patterns: associations.patterns(segments, path.basename(folder.uri.fsPath), alone),
    label: [folder.name, ...segments].join('/'),
  };
}

// The folder a command is about: the one it was called on in the
// explorer, else the workspace's, asked for when there are several.
async function chosenFolder(uri: vscode.Uri | undefined): Promise<vscode.Uri | undefined> {
  if (uri) {
    return uri;
  }
  const folders = vscode.workspace.workspaceFolders ?? [];
  if (folders.length > 1) {
    return (await vscode.window.showWorkspaceFolderPick({ placeHolder: 'The folder whose *.c and *.h files are LPC' }))?.uri;
  }
  if (folders.length === 0) {
    void vscode.window.showWarningMessage(
      'dgdtools: open the folder of the lib first (File > Open Folder). Which files are LPC is written to the settings of a workspace, never to your own.',
    );
  }
  return folders[0]?.uri;
}

// setAssociation makes the *.c and *.h files of a folder `dgdlpc`, or
// takes that back, in the settings of the workspace and in no others: on
// this machine too, a .c file is C everywhere else.
async function setAssociation(uri: vscode.Uri | undefined, on: boolean): Promise<void> {
  const dir = await chosenFolder(uri);
  if (!dir) {
    return;
  }
  const where = place(dir);
  if (!where) {
    void vscode.window.showWarningMessage(`dgdtools: ${dir.fsPath} is not in a folder that is open here, and has no workspace settings to write to.`);
    return;
  }
  const files = vscode.workspace.getConfiguration('files');
  const current = files.inspect<associations.Associations>('associations')?.workspaceValue;
  const next = on ? associations.add(current, where.patterns) : associations.remove(current, where.patterns);
  const nothing = on
    ? associations.has(current, where.patterns)
    : Object.keys(next ?? {}).length === Object.keys(current ?? {}).length;
  if (nothing) {
    void vscode.window.showInformationMessage(
      on
        ? `dgdtools: the *.c and *.h files of ${where.label} are DGD LPC already.`
        : `dgdtools: the workspace settings do not say the *.c and *.h files of ${where.label} are DGD LPC.`,
    );
    return;
  }
  await files.update('associations', next, vscode.ConfigurationTarget.Workspace);
  void vscode.window.showInformationMessage(
    on
      ? `dgdtools: the *.c and *.h files of ${where.label} are DGD LPC now (files.associations in the workspace settings).`
      : `dgdtools: the *.c and *.h files of ${where.label} are no longer DGD LPC (files.associations in the workspace settings).`,
  );
}

// offerAssociations asks, for each folder of the workspace that has a
// dgdtools.yml and whose files are not LPC yet, whether they are to be.
// Of a nest of config files the outermost is asked about: a project's
// files are everything below it.
async function offerAssociations(context: vscode.ExtensionContext): Promise<void> {
  const configs = await vscode.workspace.findFiles('**/{dgdtools.yml,.dgdtools.yml}');
  const dirs = configs.filter((u) => u.scheme === 'file').map((u) => path.dirname(u.fsPath));
  const effective = vscode.workspace.getConfiguration('files').get<associations.Associations>('associations');
  for (const dir of associations.outermost(dirs, path.sep)) {
    const uri = vscode.Uri.file(dir);
    const where = place(uri);
    const declined = `declined:${dir}`;
    if (!where || associations.has(effective, where.patterns) || context.workspaceState.get(declined)) {
      continue;
    }
    const yes = 'Yes';
    const never = 'Never';
    const choice = await vscode.window.showInformationMessage(
      `dgdtools: ${where.label} has a dgdtools.yml. Treat its *.c and *.h files as DGD LPC? This is written to the workspace settings (files.associations); everywhere else they stay C.`,
      yes,
      never,
    );
    if (choice === yes) {
      await setAssociation(uri, true);
    } else if (choice === never) {
      await context.workspaceState.update(declined, true);
    }
  }
}
