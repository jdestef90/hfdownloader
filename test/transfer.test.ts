import assert from 'node:assert/strict';
import { chmod, mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import test from 'node:test';
import { createHash } from 'node:crypto';

import {
  ChecksumError,
  downloadQuant,
  LocalTarget,
  OverwriteRequiredError,
  SshTarget,
  type DownloadProgress,
} from '../src/transfer.ts';
import { sendJson, startServer, withTempDir } from './helpers.ts';

function sha256(value: string): string {
  return createHash('sha256').update(value).digest('hex');
}

async function startFileServer(files: Record<string, { body: string; ranges?: boolean }>) {
  return startServer((request, response) => {
    const path = new URL(request.url, 'http://localhost').pathname;
    const file = files[path];
    if (!file) {
      response.statusCode = 404;
      response.end('missing');
      return;
    }
    const body = Buffer.from(file.body);
    if (request.method === 'HEAD') {
      response.setHeader('x-linked-etag', sha256(file.body));
      response.end();
      return;
    }
    const range = request.headers.range;
    if (range && file.ranges !== false) {
      const match = /^bytes=(\d+)-$/.exec(range);
      if (match) {
        const start = Number(match[1]);
        response.statusCode = 206;
        response.setHeader('content-range', `bytes ${start}-${body.length - 1}/${body.length}`);
        response.end(body.subarray(start));
        return;
      }
    }
    response.statusCode = 200;
    response.end(body);
  });
}

test('downloads a Quant into the target directory with progress', async () => {
  const server = await startFileServer({
    '/resolve/main/Model-Q4_K_M.gguf': { body: 'quant-bytes' },
  });
  try {
    await withTempDir(async (dir) => {
      const progress: DownloadProgress[] = [];
      const result = await downloadQuant({
        directory: dir,
        target: new LocalTarget(),
        overwrite: false,
        files: [
          {
            path: 'sub/Model-Q4_K_M.gguf',
            size: 11,
            sha256: sha256('quant-bytes'),
            url: `${server.baseUrl}/resolve/main/Model-Q4_K_M.gguf`,
          },
        ],
        onProgress: (value) => progress.push(value),
      });

      assert.equal(await readFile(join(dir, 'Model-Q4_K_M.gguf'), 'utf8'), 'quant-bytes');
      assert.deepEqual(result.files.map((file) => file.path), [
        join(dir, 'Model-Q4_K_M.gguf'),
      ]);
      assert.equal(result.bytes, 11);
      assert.ok(progress.length > 0);
      const last = progress.at(-1)!;
      assert.equal(last.transferred, 11);
      assert.equal(last.total, 11);
      assert.equal(last.completedFiles, 1);
      assert.ok(last.speed >= 0);
      assert.equal(last.etaSeconds, 0);
    });
  } finally {
    await server.close();
  }
});

test('requires explicit overwrite confirmation for an existing target', async () => {
  const server = await startFileServer({
    '/resolve/main/Model.gguf': { body: 'new' },
  });
  try {
    await withTempDir(async (dir) => {
      await writeFile(join(dir, 'Model.gguf'), 'old');
      await assert.rejects(
        () =>
          downloadQuant({
            directory: dir,
            target: new LocalTarget(),
            overwrite: false,
            files: [
              {
                path: 'Model.gguf',
                size: 3,
                sha256: sha256('new'),
                url: `${server.baseUrl}/resolve/main/Model.gguf`,
              },
            ],
          }),
        (error: unknown) => {
          assert.ok(error instanceof OverwriteRequiredError);
          assert.deepEqual(error.paths, [join(dir, 'Model.gguf')]);
          return true;
        },
      );
      assert.equal(await readFile(join(dir, 'Model.gguf'), 'utf8'), 'old');

      await downloadQuant({
        directory: dir,
        target: new LocalTarget(),
        overwrite: true,
        files: [
          {
            path: 'Model.gguf',
            size: 3,
            sha256: sha256('new'),
            url: `${server.baseUrl}/resolve/main/Model.gguf`,
          },
        ],
      });
      assert.equal(await readFile(join(dir, 'Model.gguf'), 'utf8'), 'new');
    });
  } finally {
    await server.close();
  }
});

test('resumes an interrupted transfer with a Range request', async () => {
  const full = 'abcdefghijklmnopqrstuvwxyz';
  const server = await startFileServer({
    '/resolve/main/Model.gguf': { body: full },
  });
  try {
    await withTempDir(async (dir) => {
      await writeFile(join(dir, 'Model.gguf.part'), full.slice(0, 10));
      await downloadQuant({
        directory: dir,
        target: new LocalTarget(),
        overwrite: false,
        files: [
          {
            path: 'Model.gguf',
            size: full.length,
            sha256: sha256(full),
            url: `${server.baseUrl}/resolve/main/Model.gguf`,
          },
        ],
      });
      assert.equal(await readFile(join(dir, 'Model.gguf'), 'utf8'), full);
      const get = server.requests.find((request) => request.method === 'GET');
      assert.equal(get?.headers.range, 'bytes=10-');
    });
  } finally {
    await server.close();
  }
});

test('restarts from zero when the server ignores Range', async () => {
  const full = 'abcdefghij';
  const server = await startFileServer({
    '/resolve/main/Model.gguf': { body: full, ranges: false },
  });
  try {
    await withTempDir(async (dir) => {
      await writeFile(join(dir, 'Model.gguf.part'), 'XXXXX');
      await downloadQuant({
        directory: dir,
        target: new LocalTarget(),
        overwrite: false,
        files: [
          {
            path: 'Model.gguf',
            size: full.length,
            sha256: sha256(full),
            url: `${server.baseUrl}/resolve/main/Model.gguf`,
          },
        ],
      });
      assert.equal(await readFile(join(dir, 'Model.gguf'), 'utf8'), full);
    });
  } finally {
    await server.close();
  }
});

test('cancelling removes partial files', async () => {
  const server = await startServer((request, response) => {
    if (request.method === 'HEAD') {
      response.statusCode = 200;
      response.end();
      return;
    }
    response.statusCode = 200;
    response.setHeader('content-type', 'application/octet-stream');
    let sent = 0;
    const timer = setInterval(() => {
      if (response.destroyed) {
        clearInterval(timer);
        return;
      }
      response.write(Buffer.alloc(1024, 1));
      sent += 1024;
      if (sent >= 64 * 1024) {
        clearInterval(timer);
        response.end();
      }
    }, 5);
    response.on('close', () => clearInterval(timer));
  });

  try {
    await withTempDir(async (dir) => {
      const controller = new AbortController();
      let aborted = false;
      await assert.rejects(
        () =>
          downloadQuant({
            directory: dir,
            target: new LocalTarget(),
            overwrite: false,
            signal: controller.signal,
            files: [
              {
                path: 'Model.gguf',
                size: 64 * 1024,
                url: `${server.baseUrl}/resolve/main/Model.gguf`,
              },
            ],
            onProgress: () => {
              if (!aborted) {
                aborted = true;
                controller.abort();
              }
            },
          }),
        /cancel|abort/i,
      );
      assert.equal(aborted, true);
      await assert.rejects(() => readFile(join(dir, 'Model.gguf')));
      await assert.rejects(() => readFile(join(dir, 'Model.gguf.part')));
    });
  } finally {
    await server.close();
  }
});

test('discards a file whose published SHA-256 does not match', async () => {
  const server = await startFileServer({
    '/resolve/main/Model.gguf': { body: 'corrupt' },
  });
  try {
    await withTempDir(async (dir) => {
      await assert.rejects(
        () =>
          downloadQuant({
            directory: dir,
            target: new LocalTarget(),
            overwrite: false,
            files: [
              {
                path: 'Model.gguf',
                size: 7,
                sha256: sha256('something else'),
                url: `${server.baseUrl}/resolve/main/Model.gguf`,
              },
            ],
          }),
        (error: unknown) => {
          assert.ok(error instanceof ChecksumError);
          return true;
        },
      );
      await assert.rejects(() => readFile(join(dir, 'Model.gguf')));
      await assert.rejects(() => readFile(join(dir, 'Model.gguf.part')));
    });
  } finally {
    await server.close();
  }
});

test('uses the Hub linked ETag when the tree has no SHA-256', async () => {
  const body = 'verified-by-head';
  const server = await startFileServer({
    '/resolve/main/Model.gguf': { body },
  });
  try {
    await withTempDir(async (dir) => {
      await downloadQuant({
        directory: dir,
        target: new LocalTarget(),
        overwrite: false,
        files: [
          {
            path: 'Model.gguf',
            size: body.length,
            url: `${server.baseUrl}/resolve/main/Model.gguf`,
          },
        ],
      });
      assert.equal(await readFile(join(dir, 'Model.gguf'), 'utf8'), body);
      assert.ok(server.requests.some((request) => request.method === 'HEAD'));
    });
  } finally {
    await server.close();
  }
});

test('SSH target writes, hashes, renames, reads and runs through the remote shell', async () => {
  await withTempDir(async (dir) => {
    const fakeSsh = join(dir, 'ssh');
    await writeFile(
      fakeSsh,
      '#!/bin/sh\nshift\nexec sh -c "$1"\n',
    );
    await chmod(fakeSsh, 0o755);

    const remoteDir = join(dir, 'remote with spaces');
    await mkdir(remoteDir);
    const target = new SshTarget({ target: 'fake@host', sshBinary: fakeSsh });
    const path = join(remoteDir, 'Model.gguf');

    await target.writeFromStream(
      path,
      (async function* () {
        yield Buffer.from('hello ');
        yield Buffer.from('world');
      })(),
      { append: false },
    );
    assert.equal((await target.stat(path))?.size, 11);
    assert.equal(await target.readText(path), 'hello world');
    assert.equal(await target.sha256(path), sha256('hello world'));

    await target.rename(path, `${path}.moved`);
    assert.equal(await target.stat(path), null);
    assert.equal((await target.stat(`${path}.moved`))?.size, 11);

    const result = await target.run('echo ran-remote');
    assert.equal(result.code, 0);
    assert.equal(result.stdout.trim(), 'ran-remote');

    await target.remove(`${path}.moved`);
    assert.equal(await target.stat(`${path}.moved`), null);
  });
});

test('downloads over the SSH target with the original Hub filename', async () => {
  const server = await startFileServer({
    '/resolve/main/Model-Q4_K_M.gguf': { body: 'ssh-quant' },
  });
  try {
    await withTempDir(async (dir) => {
      const fakeSsh = join(dir, 'ssh');
      await writeFile(fakeSsh, '#!/bin/sh\nshift\nexec sh -c "$1"\n');
      await chmod(fakeSsh, 0o755);
      const remoteDir = join(dir, 'remote');
      await mkdir(remoteDir);
      const target = new SshTarget({ target: 'fake@host', sshBinary: fakeSsh });

      await downloadQuant({
        directory: remoteDir,
        target,
        overwrite: false,
        files: [
          {
            path: 'Model-Q4_K_M.gguf',
            size: 9,
            sha256: sha256('ssh-quant'),
            url: `${server.baseUrl}/resolve/main/Model-Q4_K_M.gguf`,
          },
        ],
      });

      assert.equal(await readFile(join(remoteDir, 'Model-Q4_K_M.gguf'), 'utf8'), 'ssh-quant');
    });
  } finally {
    await server.close();
  }
});

test('rejects a download when the Hub refuses access', async () => {
  const server = await startServer((_request, response) => {
    sendJson(response, 401, { error: 'gated' });
  });
  try {
    await withTempDir(async (dir) => {
      await assert.rejects(
        () =>
          downloadQuant({
            directory: dir,
            target: new LocalTarget(),
            overwrite: false,
            files: [
              {
                path: 'Model.gguf',
                size: 1,
                sha256: sha256('x'),
                url: `${server.baseUrl}/resolve/main/Model.gguf`,
              },
            ],
          }),
        /token|access/i,
      );
    });
  } finally {
    await server.close();
  }
});
