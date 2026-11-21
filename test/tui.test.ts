import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import test from 'node:test';

import type { Config } from '../src/config.ts';
import type { RepoInfo } from '../src/hub.ts';
import { LocalTarget, OverwriteRequiredError } from '../src/transfer.ts';
import { Session, type SessionState } from '../src/session.ts';
import { TuiController, type Key } from '../src/tui/controller.ts';
import { renderApp } from '../src/tui/render.ts';
import { withTempDir } from './helpers.ts';

const repo: RepoInfo = {
  repo: 'org/Model-GGUF',
  revision: 'main',
  contextLength: 40960,
  quants: [
    {
      label: 'Q4_K_M',
      size: 4_000_000_000,
      files: [{ path: 'Model-Q4_K_M.gguf', size: 4_000_000_000 }],
      sidecars: [],
    },
    {
      label: 'Q8_0',
      size: 8_000_000_000,
      files: [{ path: 'Model-Q8_0.gguf', size: 8_000_000_000 }],
      sidecars: [],
    },
  ],
  sidecars: [
    { path: 'mmproj-Model-f16.gguf', size: 1_000_000, kind: 'mmproj' },
    { path: 'mtp-Model.gguf', size: 2_000_000, kind: 'mtp' },
  ],
};

const config: Config = {
  mode: 'local',
  routerUrl: 'http://127.0.0.1:8080',
  ggufDirectory: '/models',
  presetPath: '/presets.ini',
  restartCommand: 'restart-router',
  helperModel: 'helper',
  helperTimeoutMs: 1000,
};

class FakeHub {
  listQuants(): Promise<RepoInfo> {
    return Promise.resolve(repo);
  }
  resolveUrl(_repo: string, path: string): string {
    return `http://hub.test/${path}`;
  }
}

class FakeRouter {
  async suggestContextWindow(): Promise<{ value: number; source: 'helper' }> {
    return { value: 32768, source: 'helper' };
  }
  async waitForPreset(): Promise<boolean> {
    return true;
  }
}

function state(overrides: Partial<SessionState>): SessionState {
  return {
    screen: 'url',
    urlInput: '',
    selectedQuant: 0,
    pickerFocus: 'quants',
    sidecarCursor: 0,
    selectedSidecars: [],
    overwritePaths: [],
    restartCommand: 'restart-router',
    ...overrides,
  };
}

const enter: Key = { name: 'return', sequence: '\r' };
const escape: Key = { name: 'escape', sequence: '\x1b' };
const tab: Key = { name: 'tab', sequence: '\t' };
const up: Key = { name: 'up', sequence: '\x1b[A' };
const down: Key = { name: 'down', sequence: '\x1b[B' };
const space: Key = { name: 'space', sequence: ' ' };
const ctrlS: Key = { name: 's', ctrl: true, sequence: '\x13' };

function letter(value: string): Key {
  return { name: value, sequence: value };
}

function type(controller: TuiController, text: string): void {
  for (const char of text) {
    controller.handleKey(
      char === ' ' ? space : { name: char, sequence: char },
    );
  }
}

test('renders the Quant picker with labels, sizes and Sidecar checkboxes', () => {
  const output = renderApp(
    state({
      screen: 'picker',
      repo,
      selectedQuant: 1,
      pickerFocus: 'sidecars',
      sidecarCursor: 1,
      selectedSidecars: ['mmproj-Model-f16.gguf'],
    }),
    { width: 100 },
  );
  assert.match(output, /Repository: org\/Model-GGUF/);
  assert.match(output, /> Q8_0/);
  assert.match(output, /Q4_K_M/);
  assert.match(output, /3\.7 GiB/);
  assert.match(output, /\[x\] mmproj/);
  assert.match(output, /> \[ \] mtp/);
  assert.match(output, /Tab focus/);
});

test('renders download progress with bytes, speed and ETA', () => {
  const output = renderApp(
    state({
      screen: 'downloading',
      repo,
      progress: {
        file: 'Model-Q4_K_M.gguf',
        transferred: 1_500_000_000,
        total: 4_000_000_000,
        speed: 25_000_000,
        etaSeconds: 100,
        completedFiles: 0,
        totalFiles: 1,
      },
    }),
    { width: 100 },
  );
  assert.match(output, /Downloading Q4_K_M/);
  assert.match(output, /1\.4 GiB \/ 3\.7 GiB/);
  assert.match(output, /23\.8 MiB\/s/);
  assert.match(output, /ETA 1m 40s/);
  assert.match(output, /Esc cancel/);
});

test('renders structured and raw Preset views plus the restart command', () => {
  const presetState: SessionState = state({
    screen: 'preset',
    preset: {
      draft: {
        alias: 'Model-Q4_K_M',
        modelPath: '/models/Model-Q4_K_M.gguf',
        contextWindow: 32768,
        mmprojPath: '/models/mmproj.gguf',
        defaults: [],
      },
      raw: '[Model-Q4_K_M]\nmodel = /models/Model-Q4_K_M.gguf\nctx-size = 32768\n',
      rawMode: false,
      field: 1,
      editing: false,
      contextInput: '32768',
      contextSource: 'helper',
      suggestionPending: false,
      contextEdited: false,
    },
  });
  const structured = renderApp(presetState, { width: 100 });
  assert.match(structured, /> Context Window: 32768/);
  assert.match(structured, /suggested by helper model/);
  assert.match(structured, /mmproj: \/models\/mmproj\.gguf/);
  assert.match(structured, /Write Preset/);

  const raw = renderApp(
    {
      ...presetState,
      preset: { ...presetState.preset!, rawMode: true },
    },
    { width: 100 },
  );
  assert.match(raw, /Raw INI/);
  assert.match(raw, /\[Model-Q4_K_M\]/);

  const restart = renderApp(
    state({ screen: 'restart', preset: presetState.preset }),
    { width: 100 },
  );
  assert.match(restart, /restart-router/);
  assert.match(restart, /No model is loaded automatically/);

  const done = renderApp(
    state({
      screen: 'done',
      done: { alias: 'Model-Q4_K_M', modelPath: '/models/Model-Q4_K_M.gguf' },
    }),
    { width: 100 },
  );
  assert.match(done, /No model was loaded automatically/);
});

test('typing a URL, navigating the picker and confirming opens the Preset draft', async () => {
  await withTempDir(async (dir) => {
    const downloaded = join(dir, 'gguf', 'Model-Q4_K_M.gguf');
    const session = new Session({
      config: { ...config, ggufDirectory: join(dir, 'gguf'), presetPath: join(dir, 'p.ini') },
      hub: new FakeHub(),
      target: new LocalTarget(),
      router: new FakeRouter(),
      download: async () => ({ files: [{ path: downloaded, size: 100 }], bytes: 100 }),
    });
    await session.start();
    const controller = new TuiController(session);

    type(controller, 'https://huggingface.co/org/Model-GGUF');
    controller.handleKey(enter);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'picker');

    controller.handleKey(down);
    assert.equal(session.state.selectedQuant, 1);
    controller.handleKey(tab);
    assert.equal(session.state.pickerFocus, 'sidecars');
    controller.handleKey(space);
    assert.deepEqual(session.state.selectedSidecars, ['mmproj-Model-f16.gguf']);
    controller.handleKey(enter);
    await session.waitForIdle();

    assert.equal(session.state.screen, 'preset');
    assert.equal(session.state.preset?.draft.alias, 'Model-Q8_0');
    assert.equal(session.state.preset?.contextInput, '32768');
  });
});

test('raw view, copy defaults and Ctrl-S drive the Preset and restart screens', async () => {
  await withTempDir(async (dir) => {
    const presetPath = join(dir, 'presets.ini');
    await writeFile(presetPath, '[Existing]\nthreads = 8\nmodel = /old.gguf\n');
    const downloaded = join(dir, 'gguf', 'Model-Q4_K_M.gguf');
    const session = new Session({
      config: { ...config, ggufDirectory: join(dir, 'gguf'), presetPath, restartCommand: 'true' },
      hub: new FakeHub(),
      target: new LocalTarget(),
      router: new FakeRouter(),
      initialUrl: 'https://huggingface.co/org/Model-GGUF',
      download: async () => ({ files: [{ path: downloaded, size: 100 }], bytes: 100 }),
    });
    await session.start();
    const controller = new TuiController(session);
    controller.handleKey(enter);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'preset');

    controller.handleKey(letter('d'));
    await session.waitForIdle();
    assert.equal(
      session.state.preset?.draft.defaults.find((entry) => entry.key === 'threads')?.value,
      '8',
    );

    controller.handleKey(letter('r'));
    assert.equal(session.state.preset?.rawMode, true);
    session.setRawText('[Raw]\nmodel = /models/raw.gguf\nctx-size = 2048\n');
    controller.handleKey(escape);
    assert.equal(session.state.preset?.rawMode, false);
    assert.equal(session.state.preset?.draft.alias, 'Raw');

    controller.handleKey(ctrlS);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'restart');

    controller.handleKey(enter);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'done');
  });
});

test('overwrite and alias confirmations respond to y and n', async () => {
  await withTempDir(async (dir) => {
    const downloaded = join(dir, 'gguf', 'Model-Q4_K_M.gguf');
    const presetPath = join(dir, 'presets.ini');
    await writeFile(presetPath, '[Model-Q4_K_M]\nmodel = /old.gguf\n');
    let attempts = 0;
    const session = new Session({
      config: { ...config, ggufDirectory: join(dir, 'gguf'), presetPath, restartCommand: 'true' },
      hub: new FakeHub(),
      target: new LocalTarget(),
      router: new FakeRouter(),
      initialUrl: 'https://huggingface.co/org/Model-GGUF',
      download: async (options) => {
        if (!options.overwrite) throw new OverwriteRequiredError([downloaded]);
        return { files: [{ path: downloaded, size: 100 }], bytes: 100 };
      },
    });
    await session.start();
    const controller = new TuiController(session);

    controller.handleKey(enter);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'overwrite');
    controller.handleKey(letter('n'));
    assert.equal(session.state.screen, 'picker');

    controller.handleKey(enter);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'overwrite');
    controller.handleKey(letter('y'));
    await session.waitForIdle();
    assert.equal(session.state.screen, 'preset');

    controller.handleKey(ctrlS);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'alias-confirm');
    controller.handleKey(letter('n'));
    assert.equal(session.state.screen, 'preset');

    controller.handleKey(ctrlS);
    await session.waitForIdle();
    assert.equal(session.state.screen, 'alias-confirm');
    controller.handleKey(letter('y'));
    await session.waitForIdle();
    assert.equal(session.state.screen, 'restart');
  });
});
