import assert from 'node:assert/strict';
import test from 'node:test';

import {
  HubAuthError,
  HubClient,
  HubError,
  parseHubUrl,
} from '../src/hub.ts';
import { sendJson, startServer } from './helpers.ts';

const tree = [
  { type: 'file', path: 'README.md', size: 12, oid: 'a'.repeat(40) },
  {
    type: 'file',
    path: 'Model-Q4_K_M-00001-of-00002.gguf',
    size: 100,
    lfs: { oid: '1'.repeat(64), size: 100 },
  },
  {
    type: 'file',
    path: 'Model-Q4_K_M-00002-of-00002.gguf',
    size: 50,
    lfs: { oid: '2'.repeat(64), size: 50 },
  },
  {
    type: 'file',
    path: 'Model-Q8_0.gguf',
    size: 200,
    lfs: { oid: '3'.repeat(64), size: 200 },
  },
  {
    type: 'file',
    path: 'mmproj-Model-f16.gguf',
    size: 10,
    lfs: { oid: '4'.repeat(64), size: 10 },
  },
  {
    type: 'file',
    path: 'mtp-Model.gguf',
    size: 20,
    lfs: { oid: '5'.repeat(64), size: 20 },
  },
  {
    type: 'file',
    path: 'Model-UD-Q5_K_XL.gguf',
    size: 300,
    lfs: { oid: '6'.repeat(64), size: 300 },
  },
];

test('parses Hugging Face repository URLs and tree links', () => {
  assert.deepEqual(parseHubUrl('https://huggingface.co/unsloth/Qwen3-0.6B-GGUF'), {
    repo: 'unsloth/Qwen3-0.6B-GGUF',
    revision: 'main',
  });
  assert.deepEqual(
    parseHubUrl('https://huggingface.co/unsloth/Qwen3-0.6B-GGUF/tree/main'),
    { repo: 'unsloth/Qwen3-0.6B-GGUF', revision: 'main' },
  );
  assert.deepEqual(
    parseHubUrl('https://huggingface.co/unsloth/Qwen3-0.6B-GGUF/blob/main/README.md'),
    { repo: 'unsloth/Qwen3-0.6B-GGUF', revision: 'main' },
  );
  assert.deepEqual(parseHubUrl('  unsloth/Qwen3-0.6B-GGUF  '), {
    repo: 'unsloth/Qwen3-0.6B-GGUF',
    revision: 'main',
  });
  assert.throws(() => parseHubUrl('https://example.com/a/b'), HubError);
  assert.throws(() => parseHubUrl('https://huggingface.co/onlyone'), HubError);
});

test('lists GGUF Quants with grouped shards, Sidecars, hashes and context', async () => {
  const server = await startServer((request, response) => {
    if (request.url.startsWith('/api/models/org/Model-GGUF/tree/main')) {
      sendJson(response, 200, tree);
      return;
    }
    if (request.url.startsWith('/api/models/org/Model-GGUF')) {
      sendJson(response, 200, {
        id: 'org/Model-GGUF',
        gguf: { architecture: 'qwen3', context_length: 40960 },
      });
      return;
    }
    response.statusCode = 404;
    response.end();
  });

  try {
    const hub = new HubClient({ baseUrl: server.baseUrl });
    const info = await hub.listQuants('org/Model-GGUF');

    assert.equal(info.repo, 'org/Model-GGUF');
    assert.equal(info.contextLength, 40960);
    assert.deepEqual(
      info.quants.map((quant) => quant.label),
      ['Q4_K_M', 'Q8_0', 'UD-Q5_K_XL'],
    );

    const sharded = info.quants[0]!;
    assert.equal(sharded.files.length, 2);
    assert.equal(sharded.size, 150);
    assert.deepEqual(
      sharded.files.map((file) => file.path),
      [
        'Model-Q4_K_M-00001-of-00002.gguf',
        'Model-Q4_K_M-00002-of-00002.gguf',
      ],
    );
    assert.equal(sharded.files[0]!.sha256, '1'.repeat(64));

    assert.deepEqual(
      info.sidecars.map((sidecar) => sidecar.kind),
      ['mmproj', 'mtp'],
    );
    assert.deepEqual(
      sharded.sidecars.map((sidecar) => sidecar.path),
      ['mmproj-Model-f16.gguf', 'mtp-Model.gguf'],
    );
    assert.equal(info.quants[1]!.sidecars.length, 2);
  } finally {
    await server.close();
  }
});

test('sends an optional Hub token and surfaces gated-repo failures', async () => {
  const server = await startServer((request, response) => {
    if (request.headers.authorization !== 'Bearer hf_secret') {
      sendJson(response, 401, { error: 'Repository is gated' });
      return;
    }
    if (request.url.includes('/tree/')) {
      sendJson(response, 200, []);
      return;
    }
    sendJson(response, 200, { id: 'org/Private-GGUF' });
  });

  try {
    const hub = new HubClient({ baseUrl: server.baseUrl, token: 'hf_secret' });
    await hub.listQuants('org/Private-GGUF');
    assert.equal(server.requests[0]!.headers.authorization, 'Bearer hf_secret');

    const anonymous = new HubClient({ baseUrl: server.baseUrl });
    await assert.rejects(
      () => anonymous.listQuants('org/Private-GGUF'),
      (error: unknown) => {
        assert.ok(error instanceof HubAuthError);
        assert.match(error.message, /token/i);
        return true;
      },
    );
  } finally {
    await server.close();
  }
});

test('builds resolve URLs with encoded repository paths', () => {
  const hub = new HubClient({ baseUrl: 'https://huggingface.co' });
  assert.equal(
    hub.resolveUrl('org/Model GGUF', 'sub dir/Model-Q4_K_M.gguf', 'main'),
    'https://huggingface.co/org/Model%20GGUF/resolve/main/sub%20dir/Model-Q4_K_M.gguf?download=true',
  );
});
