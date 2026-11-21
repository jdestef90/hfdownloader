import assert from 'node:assert/strict';
import test from 'node:test';

import { RouterClient, RouterError } from '../src/router.ts';
import type { Quant } from '../src/hub.ts';
import { sendJson, startServer } from './helpers.ts';

const quant: Quant = {
  label: 'Q4_K_M',
  size: 4_000_000_000,
  files: [
    { path: 'Model-Q4_K_M.gguf', size: 4_000_000_000 },
  ],
  sidecars: [],
  contextLength: 131072,
  architecture: 'qwen3',
};

test('suggests a Context Window with a direct chat-completions request', async () => {
  const server = await startServer((_request, response) => {
    sendJson(response, 200, {
      choices: [{ message: { role: 'assistant', content: 'The suggested context window is 32768.' } }],
    });
  });
  try {
    const router = new RouterClient({
      baseUrl: server.baseUrl,
      apiKey: 'router-secret',
    });
    const suggestion = await router.suggestContextWindow({
      repo: 'org/Model-GGUF',
      quant,
      helperModel: 'Gemma-4-12b',
      fallback: 131072,
    });

    assert.deepEqual(suggestion, { value: 32768, source: 'helper' });
    const request = server.requests[0]!;
    assert.equal(request.method, 'POST');
    assert.equal(request.url, '/v1/chat/completions');
    assert.equal(request.headers.authorization, 'Bearer router-secret');
    const body = JSON.parse(request.body) as {
      model: string;
      messages: { content: string }[];
      temperature: number;
    };
    assert.equal(body.model, 'Gemma-4-12b');
    assert.equal(body.temperature, 0);
    const prompt = body.messages.map((message) => message.content).join('\n');
    assert.match(prompt, /org\/Model-GGUF/);
    assert.match(prompt, /Q4_K_M/);
    assert.match(prompt, /131072/);
    assert.match(prompt, /qwen3/);
  } finally {
    await server.close();
  }
});

test('falls back to the published Hub limit on timeout or unusable output', async () => {
  const server = await startServer(async (request, response) => {
    if (request.body.includes('org/slow')) {
      await new Promise((resolve) => setTimeout(resolve, 200));
      sendJson(response, 200, { choices: [{ message: { content: '32768' } }] });
      return;
    }
    if (request.body.includes('org/error')) {
      sendJson(response, 500, { error: 'boom' });
      return;
    }
    sendJson(response, 200, { choices: [{ message: { content: 'no idea' } }] });
  });
  try {
    const router = new RouterClient({ baseUrl: server.baseUrl });
    const base = {
      repo: 'org/Model-GGUF',
      quant,
      helperModel: 'helper',
      fallback: 131072,
    };

    const timeout = await router.suggestContextWindow({
      ...base,
      repo: 'org/slow',
      timeoutMs: 20,
    });
    assert.deepEqual(timeout, { value: 131072, source: 'hub' });

    const unusable = await router.suggestContextWindow(base);
    assert.deepEqual(unusable, { value: 131072, source: 'hub' });

    const failed = await router.suggestContextWindow({ ...base, repo: 'org/error' });
    assert.deepEqual(failed, { value: 131072, source: 'hub' });

    const none = await router.suggestContextWindow({ ...base, fallback: undefined });
    assert.deepEqual(none, { value: null, source: 'none' });
  } finally {
    await server.close();
  }
});

test('does not swallow caller cancellation', async () => {
  const server = await startServer(async (_request, response) => {
    await new Promise((resolve) => setTimeout(resolve, 200));
    sendJson(response, 200, { choices: [{ message: { content: '32768' } }] });
  });
  try {
    const router = new RouterClient({ baseUrl: server.baseUrl });
    const controller = new AbortController();
    controller.abort();
    await assert.rejects(
      () =>
        router.suggestContextWindow({
          repo: 'org/Model-GGUF',
          quant,
          helperModel: 'helper',
          fallback: 131072,
          signal: controller.signal,
        }),
      (error: unknown) => {
        assert.equal((error as Error).name, 'AbortError');
        return true;
      },
    );
  } finally {
    await server.close();
  }
});

test('reports whether a written Preset is visible on the Router', async () => {
  let calls = 0;
  const server = await startServer((_request, response) => {
    calls += 1;
    if (calls === 1) {
      sendJson(response, 200, { data: [] });
      return;
    }
    sendJson(response, 200, { data: [{ id: 'Model-Q4_K_M' }] });
  });
  try {
    const router = new RouterClient({ baseUrl: server.baseUrl, apiKey: 'key' });
    const visible = await router.waitForPreset('Model-Q4_K_M', { timeoutMs: 500, intervalMs: 10 });
    assert.equal(visible, true);
    assert.equal(server.requests[0]!.headers.authorization, 'Bearer key');
  } finally {
    await server.close();
  }
});

test('surfaces Router failures with a useful error', async () => {
  const server = await startServer((_request, response) => {
    response.statusCode = 503;
    response.end('offline');
  });
  try {
    const router = new RouterClient({ baseUrl: server.baseUrl });
    await assert.rejects(
      () => router.listPresets(),
      (error: unknown) => {
        assert.ok(error instanceof RouterError);
        assert.match(error.message, /503/);
        return true;
      },
    );
  } finally {
    await server.close();
  }
});
