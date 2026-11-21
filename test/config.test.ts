import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';

import { ConfigError, loadConfig } from '../src/config.ts';

async function withTempDir<T>(fn: (dir: string) => Promise<T>): Promise<T> {
  const dir = await mkdtemp(join(tmpdir(), 'hfdownloader-config-'));
  try {
    return await fn(dir);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

const completeConfig = {
  mode: 'ssh',
  routerUrl: 'http://router.local:8080',
  sshTarget: 'justin@router.local',
  ggufDirectory: '/Users/justin/models/gguf',
  presetPath: '/Users/justin/.config/llama/presets.ini',
  restartCommand: 'brew services restart llama.cpp',
  helperModel: 'Gemma-4-12b',
};

test('loads configuration from one JSON file', async () => {
  await withTempDir(async (dir) => {
    const path = join(dir, 'config.json');
    await writeFile(path, JSON.stringify(completeConfig));

    const config = loadConfig({ env: {}, configPath: path });

    assert.equal(config.mode, 'ssh');
    assert.equal(config.routerUrl, 'http://router.local:8080');
    assert.equal(config.sshTarget, 'justin@router.local');
    assert.equal(config.ggufDirectory, '/Users/justin/models/gguf');
    assert.equal(config.presetPath, '/Users/justin/.config/llama/presets.ini');
    assert.equal(config.restartCommand, 'brew services restart llama.cpp');
    assert.equal(config.helperModel, 'Gemma-4-12b');
    assert.equal(config.hubToken, undefined);
    assert.equal(config.routerApiKey, undefined);
  });
});

test('environment variables override file values', async () => {
  await withTempDir(async (dir) => {
    const path = join(dir, 'config.json');
    await writeFile(path, JSON.stringify(completeConfig));

    const config = loadConfig({
      configPath: path,
      env: {
        HFDOWNLOADER_MODE: 'local',
        HFDOWNLOADER_ROUTER_URL: 'http://127.0.0.1:9090',
        HFDOWNLOADER_GGUF_DIRECTORY: '/models',
        HFDOWNLOADER_PRESET_PATH: '/etc/presets.ini',
        HFDOWNLOADER_RESTART_COMMAND: 'true',
        HFDOWNLOADER_HELPER_MODEL: 'helper',
        HFDOWNLOADER_HUB_TOKEN: 'hf_secret',
        HFDOWNLOADER_ROUTER_API_KEY: 'router_secret',
      },
    });

    assert.equal(config.mode, 'local');
    assert.equal(config.routerUrl, 'http://127.0.0.1:9090');
    assert.equal(config.ggufDirectory, '/models');
    assert.equal(config.presetPath, '/etc/presets.ini');
    assert.equal(config.restartCommand, 'true');
    assert.equal(config.helperModel, 'helper');
    assert.equal(config.hubToken, 'hf_secret');
    assert.equal(config.routerApiKey, 'router_secret');
    // An overridden mode must not require the SSH target from the file.
    assert.equal(config.sshTarget, 'justin@router.local');
  });
});

test('defaults the Router URL and mode', async () => {
  await withTempDir(async (dir) => {
    const path = join(dir, 'config.json');
    await writeFile(
      path,
      JSON.stringify({
        sshTarget: 'justin@router.local',
        ggufDirectory: '/models',
        presetPath: '/presets.ini',
        restartCommand: 'restart',
        helperModel: 'helper',
      }),
    );

    const config = loadConfig({ env: {}, configPath: path });

    assert.equal(config.mode, 'ssh');
    assert.equal(config.routerUrl, 'http://127.0.0.1:8080');
  });
});

test('reports missing required settings together', async () => {
  await withTempDir(async (dir) => {
    const path = join(dir, 'config.json');
    await writeFile(path, JSON.stringify({ mode: 'ssh' }));

    assert.throws(
      () => loadConfig({ env: {}, configPath: path }),
      (error: unknown) => {
        assert.ok(error instanceof ConfigError);
        assert.match(error.message, /sshTarget/);
        assert.match(error.message, /ggufDirectory/);
        assert.match(error.message, /presetPath/);
        assert.match(error.message, /restartCommand/);
        assert.match(error.message, /helperModel/);
        return true;
      },
    );
  });
});

test('local mode does not require an SSH target', async () => {
  await withTempDir(async (dir) => {
    const path = join(dir, 'config.json');
    await writeFile(
      path,
      JSON.stringify({
        mode: 'local',
        ggufDirectory: '/models',
        presetPath: '/presets.ini',
        restartCommand: 'restart',
        helperModel: 'helper',
      }),
    );

    const config = loadConfig({ env: {}, configPath: path });
    assert.equal(config.mode, 'local');
    assert.equal(config.sshTarget, undefined);
  });
});

test('rejects an unknown mode and a malformed file', async () => {
  await withTempDir(async (dir) => {
    const path = join(dir, 'config.json');
    await writeFile(path, JSON.stringify({ ...completeConfig, mode: 'sneaker' }));
    assert.throws(() => loadConfig({ env: {}, configPath: path }), /mode/i);

    await writeFile(path, '{ not json');
    assert.throws(() => loadConfig({ env: {}, configPath: path }), /config/i);
  });
});

test('uses HFDOWNLOADER_CONFIG when no path is supplied', async () => {
  await withTempDir(async (dir) => {
    const path = join(dir, 'custom.json');
    await writeFile(path, JSON.stringify(completeConfig));

    const config = loadConfig({ env: { HFDOWNLOADER_CONFIG: path } });
    assert.equal(config.routerUrl, 'http://router.local:8080');
  });
});
