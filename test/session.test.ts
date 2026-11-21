import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import test from 'node:test';

import type { Config } from '../src/config.ts';
import { HubClient, type RepoInfo } from '../src/hub.ts';
import { LocalTarget, SshTarget } from '../src/transfer.ts';
import { RouterClient } from '../src/router.ts';
import { createTarget } from '../src/runtime.ts';
import { Session } from '../src/session.ts';
import { sendJson, startServer, withTempDir } from './helpers.ts';

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex');
}

function baseConfig(overrides: Partial<Config> = {}): Config {
  return {
    mode: 'local',
    routerUrl: 'http://127.0.0.1:8080',
    ggufDirectory: '/models',
    presetPath: '/presets.ini',
    restartCommand: 'true',
    helperModel: 'helper',
    helperTimeoutMs: 1_000,
    ...overrides,
  };
}

const repo: RepoInfo = {
  repo: 'org/Model-GGUF',
  revision: 'main',
  contextLength: 40960,
  quants: [
    {
      label: 'Q4_K_M',
      size: 100,
      contextLength: 40960,
      files: [{ path: 'Model-Q4_K_M.gguf', size: 100, sha256: '1'.repeat(64) }],
      sidecars: [
        { path: 'mmproj-Model-f16.gguf', size: 10, kind: 'mmproj' },
        { path: 'mtp-Model.gguf', size: 20, kind: 'mtp' },
      ],
    },
    {
      label: 'Q8_0',
      size: 200,
      files: [{ path: 'Model-Q8_0.gguf', size: 200 }],
      sidecars: [],
    },
  ],
  sidecars: [
    { path: 'mmproj-Model-f16.gguf', size: 10, kind: 'mmproj' },
    { path: 'mtp-Model.gguf', size: 20, kind: 'mtp' },
  ],
};

class FakeHub {
  readonly repo = repo;
  listQuants(): Promise<RepoInfo> {
    return Promise.resolve(this.repo);
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

test('a URL argument and a pasted URL open the same Quant picker', async () => {
  await withTempDir(async (dir) => {
    const config = baseConfig({ ggufDirectory: dir, presetPath: join(dir, 'presets.ini') });
    const argSession = new Session({
      config,
      hub: new FakeHub(),
      target: new LocalTarget(),
      router: new FakeRouter(),
      initialUrl: 'https://huggingface.co/org/Model-GGUF',
    });
    await argSession.start();
    assert.equal(argSession.state.screen, 'picker');
    assert.equal(argSession.state.repo?.repo, 'org/Model-GGUF');

    const pasteSession = new Session({
      config,
      hub: new FakeHub(),
      target: new LocalTarget(),
      router: new FakeRouter(),
    });
    await pasteSession.start();
    assert.equal(pasteSession.state.screen, 'url');
    pasteSession.setUrlInput('https://huggingface.co/org/Model-GGUF');
    await pasteSession.submitUrl();

    assert.equal(pasteSession.state.screen, 'picker');
    assert.deepEqual(
      pasteSession.state.repo?.quants.map((quant) => quant.label),
      argSession.state.repo?.quants.map((quant) => quant.label),
    );
  });
});

test('full session: Hub URL to confirmed restart with a Preset and Sidecars', async () => {
  await withTempDir(async (dir) => {
    const ggufDir = join(dir, 'gguf');
    const presetPath = join(dir, 'presets.ini');
    const restartMarker = join(dir, 'restarted');
    await writeFile(
      presetPath,
      '[Existing]\nthreads = 8\nn-gpu-layers = 99\nmodel = /old.gguf\nctx-size = 4096\n',
    );

    const quantBody = 'quant-bytes';
    const mmprojBody = 'mmproj-bytes';
    const mtpBody = 'mtp-bytes';
    const tree = [
      {
        type: 'file',
        path: 'Model-Q4_K_M.gguf',
        size: quantBody.length,
        lfs: { oid: sha256(quantBody), size: quantBody.length },
      },
      {
        type: 'file',
        path: 'mmproj-Model-f16.gguf',
        size: mmprojBody.length,
        lfs: { oid: sha256(mmprojBody), size: mmprojBody.length },
      },
      {
        type: 'file',
        path: 'mtp-Model.gguf',
        size: mtpBody.length,
        lfs: { oid: sha256(mtpBody), size: mtpBody.length },
      },
    ];
    let chatCalls = 0;

    const server = await startServer(async (request, response) => {
      const url = new URL(request.url, 'http://localhost');
      if (url.pathname.startsWith('/api/models/org/Model-GGUF/tree/main')) {
        sendJson(response, 200, tree);
        return;
      }
      if (url.pathname === '/api/models/org/Model-GGUF') {
        sendJson(response, 200, {
          id: 'org/Model-GGUF',
          gguf: { architecture: 'qwen3', context_length: 40960 },
        });
        return;
      }
      if (url.pathname === '/org/Model-GGUF/resolve/main/Model-Q4_K_M.gguf') {
        response.setHeader('x-linked-etag', sha256(quantBody));
        response.end(request.method === 'HEAD' ? undefined : quantBody);
        return;
      }
      if (url.pathname === '/org/Model-GGUF/resolve/main/mmproj-Model-f16.gguf') {
        response.end(request.method === 'HEAD' ? undefined : mmprojBody);
        return;
      }
      if (url.pathname === '/org/Model-GGUF/resolve/main/mtp-Model.gguf') {
        response.end(request.method === 'HEAD' ? undefined : mtpBody);
        return;
      }
      if (url.pathname === '/v1/chat/completions') {
        chatCalls += 1;
        sendJson(response, 200, {
          choices: [{ message: { role: 'assistant', content: '32768' } }],
        });
        return;
      }
      if (url.pathname === '/v1/models') {
        let aliases: string[] = [];
        try {
          const text = await readFile(presetPath, 'utf8');
          aliases = [...text.matchAll(/^\s*\[([^\]]+)\]/gm)].map((match) =>
            (match[1] ?? '').trim(),
          );
        } catch {
          aliases = [];
        }
        sendJson(response, 200, { data: aliases.map((id) => ({ id })) });
        return;
      }
      response.statusCode = 404;
      response.end();
    });

    try {
      const config = baseConfig({
        ggufDirectory: ggufDir,
        presetPath,
        restartCommand: `touch ${restartMarker}`,
        routerUrl: server.baseUrl,
      });
      const session = new Session({
        config,
        hub: new HubClient({ baseUrl: server.baseUrl }),
        target: new LocalTarget(),
        router: new RouterClient({ baseUrl: server.baseUrl }),
        initialUrl: 'https://huggingface.co/org/Model-GGUF',
      });

      await session.start();
      assert.equal(session.state.screen, 'picker');
      assert.equal(session.state.repo?.quants[0]?.label, 'Q4_K_M');

      session.setPickerFocus('sidecars');
      session.toggleSidecar('mmproj-Model-f16.gguf');
      session.toggleSidecar('mtp-Model.gguf');
      assert.deepEqual(session.state.selectedSidecars, [
        'mmproj-Model-f16.gguf',
        'mtp-Model.gguf',
      ]);

      await session.confirmQuant();
      assert.equal(session.state.screen, 'preset');
      assert.equal(chatCalls, 1);
      assert.equal(session.state.preset?.contextInput, '32768');
      assert.equal(session.state.preset?.contextSource, 'helper');
      assert.equal(session.state.preset?.suggestionPending, false);
      assert.equal(
        session.state.preset?.draft.modelPath,
        join(ggufDir, 'Model-Q4_K_M.gguf'),
      );
      assert.equal(
        session.state.preset?.draft.mmprojPath,
        join(ggufDir, 'mmproj-Model-f16.gguf'),
      );
      assert.equal(
        session.state.preset?.draft.mtpPath,
        join(ggufDir, 'mtp-Model.gguf'),
      );
      assert.equal(
        session.state.preset?.draft.defaults.find((entry) => entry.key === 'threads')?.value,
        '8',
      );

      session.setAlias('MyQuant');
      session.setContextWindow(16384);
      await session.writePresetNow();
      assert.equal(session.state.screen, 'restart');
      assert.equal(session.state.preset?.visible, true);

      const preset = await readFile(presetPath, 'utf8');
      assert.match(preset, /^\[MyQuant\]$/m);
      assert.match(preset, /^ctx-size = 16384$/m);
      assert.match(preset, /^threads = 8$/m);
      assert.match(preset, /^model = .*Model-Q4_K_M\.gguf$/m);
      assert.match(preset, /^mmproj = .*mmproj-Model-f16\.gguf$/m);
      assert.match(preset, /^model-draft = .*mtp-Model\.gguf$/m);
      assert.match(preset, /^spec-type = draft-mtp$/m);
      assert.match(preset, /^\[Existing\]$/m);

      const backup = session.state.preset?.backupPath;
      assert.ok(backup);
      assert.match(await readFile(backup, 'utf8'), /^\[Existing\]$/m);

      assert.equal(session.state.restartCommand, `touch ${restartMarker}`);
      await session.confirmRestart();
      assert.equal(session.state.screen, 'done');
      await readFile(restartMarker);

      const modelPosts = server.requests.filter(
        (request) =>
          request.method === 'POST' && new URL(request.url, 'http://localhost').pathname === '/models',
      );
      assert.deepEqual(modelPosts, []);
      assert.equal(chatCalls, 1);
    } finally {
      await server.close();
    }
  });
});

test('an existing target requires overwrite confirmation and keeps the old file on cancel', async () => {
  await withTempDir(async (dir) => {
    const ggufDir = join(dir, 'gguf');
    const body = 'replacement';
    const server = await startServer((request, response) => {
      const url = new URL(request.url, 'http://localhost');
      if (url.pathname.startsWith('/api/models/')) {
        if (url.pathname.includes('/tree/')) {
          sendJson(response, 200, [
            {
              type: 'file',
              path: 'Model-Q4_K_M.gguf',
              size: body.length,
              lfs: { oid: sha256(body), size: body.length },
            },
          ]);
        } else {
          sendJson(response, 200, { gguf: { context_length: 4096 } });
        }
        return;
      }
      if (url.pathname.endsWith('/resolve/main/Model-Q4_K_M.gguf')) {
        response.end(request.method === 'HEAD' ? undefined : body);
        return;
      }
      response.statusCode = 404;
      response.end();
    });
    try {
      await writeFile(join(dir, 'presets.ini'), '');
      const session = new Session({
        config: baseConfig({
          ggufDirectory: ggufDir,
          presetPath: join(dir, 'presets.ini'),
          routerUrl: server.baseUrl,
        }),
        hub: new HubClient({ baseUrl: server.baseUrl }),
        target: new LocalTarget(),
        router: new FakeRouter(),
        initialUrl: 'https://huggingface.co/org/Model-GGUF',
      });
      await session.start();
      const target = join(ggufDir, 'Model-Q4_K_M.gguf');
      await mkdir(ggufDir, { recursive: true });
      await writeFile(target, 'old');

      await session.confirmQuant();
      assert.equal(session.state.screen, 'overwrite');
      assert.deepEqual(session.state.overwritePaths, [target]);
      session.cancelOverwrite();
      assert.equal(session.state.screen, 'picker');
      assert.equal(await readFile(target, 'utf8'), 'old');

      await session.confirmQuant();
      assert.equal(session.state.screen, 'overwrite');
      await session.confirmOverwrite();
      assert.equal(session.state.screen, 'preset');
      assert.equal(await readFile(target, 'utf8'), body);
    } finally {
      await server.close();
    }
  });
});

test('a checksum mismatch discards the file and prevents a Preset', async () => {
  await withTempDir(async (dir) => {
    const server = await startServer((request, response) => {
      const url = new URL(request.url, 'http://localhost');
      if (url.pathname.includes('/tree/')) {
        sendJson(response, 200, [
          {
            type: 'file',
            path: 'Model-Q4_K_M.gguf',
            size: 7,
            lfs: { oid: sha256('not-the-body'), size: 7 },
          },
        ]);
        return;
      }
      if (url.pathname.startsWith('/api/models/')) {
        sendJson(response, 200, { gguf: { context_length: 4096 } });
        return;
      }
      if (url.pathname.endsWith('/resolve/main/Model-Q4_K_M.gguf')) {
        response.end(request.method === 'HEAD' ? undefined : 'corrupt');
        return;
      }
      response.statusCode = 404;
      response.end();
    });
    try {
      const session = new Session({
        config: baseConfig({
          ggufDirectory: join(dir, 'gguf'),
          presetPath: join(dir, 'presets.ini'),
          routerUrl: server.baseUrl,
        }),
        hub: new HubClient({ baseUrl: server.baseUrl }),
        target: new LocalTarget(),
        router: new FakeRouter(),
        initialUrl: 'https://huggingface.co/org/Model-GGUF',
      });
      await session.start();
      await session.confirmQuant();
      assert.equal(session.state.screen, 'error');
      assert.match(session.state.error?.message ?? '', /no Preset/i);
      assert.equal(session.state.preset, undefined);
      await assert.rejects(() => readFile(join(dir, 'gguf', 'Model-Q4_K_M.gguf')));
      await assert.rejects(() => readFile(join(dir, 'presets.ini')));
    } finally {
      await server.close();
    }
  });
});

test('an existing alias needs confirmation and writes a backup', async () => {
  await withTempDir(async (dir) => {
    const body = 'quant';
    const presetPath = join(dir, 'presets.ini');
    await writeFile(presetPath, '[MyQuant]\nmodel = /old.gguf\nctx-size = 1024\n');
    const server = await startServer((request, response) => {
      const url = new URL(request.url, 'http://localhost');
      if (url.pathname.includes('/tree/')) {
        sendJson(response, 200, [
          {
            type: 'file',
            path: 'Model-Q4_K_M.gguf',
            size: body.length,
            lfs: { oid: sha256(body), size: body.length },
          },
        ]);
        return;
      }
      if (url.pathname.startsWith('/api/models/')) {
        sendJson(response, 200, { gguf: { context_length: 4096 } });
        return;
      }
      if (url.pathname.endsWith('/resolve/main/Model-Q4_K_M.gguf')) {
        response.end(request.method === 'HEAD' ? undefined : body);
        return;
      }
      if (url.pathname === '/v1/models') {
        sendJson(response, 200, { data: [{ id: 'MyQuant' }] });
        return;
      }
      response.statusCode = 404;
      response.end();
    });
    try {
      const session = new Session({
        config: baseConfig({
          ggufDirectory: join(dir, 'gguf'),
          presetPath,
          routerUrl: server.baseUrl,
        }),
        hub: new HubClient({ baseUrl: server.baseUrl }),
        target: new LocalTarget(),
        router: new RouterClient({ baseUrl: server.baseUrl }),
        initialUrl: 'https://huggingface.co/org/Model-GGUF',
      });
      await session.start();
      await session.confirmQuant();
      session.setAlias('MyQuant');
      session.setContextWindow(8192);
      await session.writePresetNow();
      assert.equal(session.state.screen, 'alias-confirm');

      await session.confirmAliasOverwrite();
      assert.equal(session.state.screen, 'restart');
      assert.ok(session.state.preset?.backupPath);
      assert.match(await readFile(presetPath, 'utf8'), /^ctx-size = 8192$/m);
      assert.match(
        await readFile(session.state.preset!.backupPath!, 'utf8'),
        /^\[MyQuant\]$/m,
      );
    } finally {
      await server.close();
    }
  });
});

test('raw INI edits stay in sync with the structured Preset fields', async () => {
  await withTempDir(async (dir) => {
    const body = 'quant';
    const server = await startServer((request, response) => {
      const url = new URL(request.url, 'http://localhost');
      if (url.pathname.includes('/tree/')) {
        sendJson(response, 200, [
          {
            type: 'file',
            path: 'Model-Q4_K_M.gguf',
            size: body.length,
            lfs: { oid: sha256(body), size: body.length },
          },
        ]);
        return;
      }
      if (url.pathname.startsWith('/api/models/')) {
        sendJson(response, 200, { gguf: { context_length: 4096 } });
        return;
      }
      if (url.pathname.endsWith('/resolve/main/Model-Q4_K_M.gguf')) {
        response.end(request.method === 'HEAD' ? undefined : body);
        return;
      }
      response.statusCode = 404;
      response.end();
    });
    try {
      const presetPath = join(dir, 'presets.ini');
      const session = new Session({
        config: baseConfig({
          ggufDirectory: join(dir, 'gguf'),
          presetPath,
          routerUrl: server.baseUrl,
        }),
        hub: new HubClient({ baseUrl: server.baseUrl }),
        target: new LocalTarget(),
        router: new FakeRouter(),
        initialUrl: 'https://huggingface.co/org/Model-GGUF',
      });
      await session.start();
      await session.confirmQuant();
      session.toggleRawMode();
      assert.equal(session.state.preset?.rawMode, true);
      session.setRawText(
        '[RawAlias]\nmodel = /models/Model-Q4_K_M.gguf\nctx-size = 24576\nthreads = 12\n',
      );
      assert.equal(session.applyRawText(), true);
      assert.equal(session.state.preset?.rawMode, false);
      assert.equal(session.state.preset?.draft.alias, 'RawAlias');
      assert.equal(session.state.preset?.contextInput, '24576');
      await session.writePresetNow();
      assert.match(await readFile(presetPath, 'utf8'), /^\[RawAlias\]$/m);
      assert.match(await readFile(presetPath, 'utf8'), /^threads = 12$/m);
    } finally {
      await server.close();
    }
  });
});

test('mode selects direct local writes or SSH writes without changing the filename', () => {
  const local = createTarget(baseConfig({ mode: 'local' }));
  assert.ok(local instanceof LocalTarget);

  const ssh = createTarget(
    baseConfig({ mode: 'ssh', sshTarget: 'justin@router.local' }),
  );
  assert.ok(ssh instanceof SshTarget);
  assert.equal(ssh.description, 'ssh justin@router.local');
});
