import { createHash } from 'node:crypto';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { HubClient } from '../src/hub.ts';
import { LocalTarget } from '../src/transfer.ts';
import { RouterClient } from '../src/router.ts';
import { Session } from '../src/session.ts';
import { renderApp } from '../src/tui/render.ts';

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex');
}

function show(label: string, session: Session): void {
  process.stdout.write(`\n=== ${label} ===\n`);
  process.stdout.write(renderApp(session.state, { width: 88 }));
}

const dir = await mkdtemp(join(tmpdir(), 'hfdownloader-demo-'));
const ggufDirectory = join(dir, 'gguf');
const presetPath = join(dir, 'presets.ini');
const marker = join(dir, 'restarted');
const quant = 'demo quant bytes';
const mmproj = 'demo mmproj bytes';
const tree = [
  {
    type: 'file',
    path: 'Demo-Q4_K_M.gguf',
    size: quant.length,
    lfs: { oid: sha256(quant), size: quant.length },
  },
  {
    type: 'file',
    path: 'mmproj-Demo-f16.gguf',
    size: mmproj.length,
    lfs: { oid: sha256(mmproj), size: mmproj.length },
  },
];

const { createServer } = await import('node:http');
const server = createServer(async (request, response) => {
  const url = new URL(request.url ?? '/', 'http://localhost');
  const json = (value: unknown): void => {
    response.setHeader('content-type', 'application/json');
    response.end(JSON.stringify(value));
  };
  if (url.pathname.startsWith('/api/models/org/Demo-GGUF/tree/main')) return json(tree);
  if (url.pathname === '/api/models/org/Demo-GGUF') {
    return json({ gguf: { architecture: 'demo', context_length: 40960 } });
  }
  if (url.pathname === '/org/Demo-GGUF/resolve/main/Demo-Q4_K_M.gguf') {
    response.setHeader('x-linked-etag', sha256(quant));
    return response.end(request.method === 'HEAD' ? undefined : quant);
  }
  if (url.pathname === '/org/Demo-GGUF/resolve/main/mmproj-Demo-f16.gguf') {
    return response.end(request.method === 'HEAD' ? undefined : mmproj);
  }
  if (url.pathname === '/v1/chat/completions') {
    return json({ choices: [{ message: { content: '32768' } }] });
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
    return json({ data: aliases.map((id) => ({ id })) });
  }
  response.statusCode = 404;
  response.end('not found');
});

await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
const address = server.address();
if (address === null || typeof address === 'string') throw new Error('demo server failed');
const baseUrl = `http://127.0.0.1:${address.port}`;

try {
  const session = new Session({
    config: {
      mode: 'local',
      routerUrl: baseUrl,
      ggufDirectory,
      presetPath,
      restartCommand: `touch ${marker}`,
      helperModel: 'demo-helper',
      helperTimeoutMs: 2000,
    },
    hub: new HubClient({ baseUrl }),
    target: new LocalTarget(),
    router: new RouterClient({ baseUrl }),
    initialUrl: 'https://huggingface.co/org/Demo-GGUF',
  });

  await session.start();
  show('Picker', session);

  session.toggleSidecar('mmproj-Demo-f16.gguf');
  await session.confirmQuant();
  show('Preset draft with suggested Context Window', session);

  session.setAlias('Demo-Q4_K_M');
  session.setContextWindow(16384);
  await session.writePresetNow();
  show('Restart confirmation', session);

  await session.confirmRestart();
  show('Done', session);
  await readFile(marker);
  process.stdout.write('\nDemo complete: Preset written, restart ran, no model loaded.\n');
} finally {
  await new Promise<void>((resolve) => server.close(() => resolve()));
  await rm(dir, { recursive: true, force: true });
}
