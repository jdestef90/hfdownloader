import { createHash } from 'node:crypto';
import { createReadStream, createWriteStream } from 'node:fs';
import { mkdir, readFile, rename, rm, stat, writeFile } from 'node:fs/promises';
import { dirname, posix } from 'node:path';
import { once } from 'node:events';
import { spawn } from 'node:child_process';

import { HubAuthError } from './hub.ts';

export interface CommandResult {
  code: number;
  stdout: string;
  stderr: string;
}

export interface Target {
  readonly description: string;
  stat(path: string): Promise<{ size: number } | null>;
  writeFromStream(
    path: string,
    body: AsyncIterable<Uint8Array>,
    options: { append: boolean; signal?: AbortSignal },
  ): Promise<void>;
  rename(from: string, to: string): Promise<void>;
  remove(path: string): Promise<void>;
  sha256(path: string): Promise<string>;
  readText(path: string): Promise<string | null>;
  writeText(path: string, content: string): Promise<void>;
  run(command: string, options?: { signal?: AbortSignal }): Promise<CommandResult>;
}

export interface DownloadFile {
  path: string;
  size: number;
  sha256?: string;
  url: string;
}

export interface DownloadProgress {
  file: string;
  transferred: number;
  total: number;
  speed: number;
  etaSeconds: number;
  completedFiles: number;
  totalFiles: number;
}

export interface DownloadOptions {
  files: DownloadFile[];
  target: Target;
  directory: string;
  overwrite: boolean;
  onProgress?: (progress: DownloadProgress) => void;
  signal?: AbortSignal;
  fetch?: typeof fetch;
  headers?: Record<string, string>;
  now?: () => number;
}

export interface DownloadResult {
  files: { path: string; size: number }[];
  bytes: number;
}

export class DownloadError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'DownloadError';
  }
}

export class OverwriteRequiredError extends DownloadError {
  readonly paths: string[];
  constructor(paths: string[]) {
    super(`These files already exist and need overwrite confirmation: ${paths.join(', ')}`);
    this.name = 'OverwriteRequiredError';
    this.paths = paths;
  }
}

export class ChecksumError extends DownloadError {
  readonly path: string;
  readonly expected: string;
  readonly actual: string;
  constructor(path: string, expected: string, actual: string) {
    super(`SHA-256 mismatch for ${path}: expected ${expected}, got ${actual}`);
    this.name = 'ChecksumError';
    this.path = path;
    this.expected = expected;
    this.actual = actual;
  }
}

function abortError(): Error {
  const error = new Error('Download cancelled');
  error.name = 'AbortError';
  return error;
}

function joinRemote(directory: string, name: string): string {
  const trimmed = directory.replace(/\/+$/, '');
  return trimmed === '' ? name : `${trimmed}/${name}`;
}

function shellQuote(value: string): string {
  return `'${value.replace(/'/g, `'\\''`)}'`;
}

export class LocalTarget implements Target {
  readonly description = 'local filesystem';

  async stat(path: string): Promise<{ size: number } | null> {
    try {
      const value = await stat(path);
      return value.isFile() ? { size: value.size } : null;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
      throw error;
    }
  }

  async writeFromStream(
    path: string,
    body: AsyncIterable<Uint8Array>,
    options: { append: boolean; signal?: AbortSignal },
  ): Promise<void> {
    await mkdir(dirname(path), { recursive: true });
    if (options.signal?.aborted) throw abortError();

    const stream = createWriteStream(path, { flags: options.append ? 'a' : 'w' });
    const onAbort = () => stream.destroy(abortError());
    options.signal?.addEventListener('abort', onAbort, { once: true });
    try {
      await new Promise<void>((resolve, reject) => {
        stream.on('error', reject);
        stream.on('finish', resolve);
        void (async () => {
          try {
            for await (const chunk of body) {
              if (options.signal?.aborted) throw abortError();
              if (!stream.write(chunk)) {
                await once(stream, 'drain');
              }
            }
            stream.end();
          } catch (error) {
            stream.destroy();
            reject(error);
          }
        })();
      });
    } finally {
      options.signal?.removeEventListener('abort', onAbort);
    }
  }

  async rename(from: string, to: string): Promise<void> {
    await mkdir(dirname(to), { recursive: true });
    await rename(from, to);
  }

  async remove(path: string): Promise<void> {
    await rm(path, { force: true });
  }

  async sha256(path: string): Promise<string> {
    const hash = createHash('sha256');
    for await (const chunk of createReadStream(path)) {
      hash.update(chunk as Buffer);
    }
    return hash.digest('hex');
  }

  async readText(path: string): Promise<string | null> {
    try {
      return await readFile(path, 'utf8');
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code === 'ENOENT') return null;
      throw error;
    }
  }

  async writeText(path: string, content: string): Promise<void> {
    await mkdir(dirname(path), { recursive: true });
    await writeFile(path, content, 'utf8');
  }

  async run(command: string, options: { signal?: AbortSignal } = {}): Promise<CommandResult> {
    return runProcess('sh', ['-c', command], options);
  }
}

export interface SshTargetOptions {
  target: string;
  sshBinary?: string;
}

export class SshTarget implements Target {
  readonly description: string;
  readonly #target: string;
  readonly #ssh: string;

  constructor(options: SshTargetOptions) {
    this.#target = options.target;
    this.#ssh = options.sshBinary ?? 'ssh';
    this.description = `ssh ${options.target}`;
  }

  async stat(path: string): Promise<{ size: number } | null> {
    const quoted = shellQuote(path);
    const result = await this.run(
      `if [ -f ${quoted} ]; then wc -c < ${quoted}; else echo MISSING; fi`,
    );
    if (result.code !== 0) {
      throw new DownloadError(`Cannot stat ${path} over SSH: ${result.stderr.trim()}`);
    }
    const output = result.stdout.trim();
    if (output === 'MISSING') return null;
    const size = Number(output);
    if (!Number.isFinite(size)) {
      throw new DownloadError(`Unexpected stat output for ${path}: ${output}`);
    }
    return { size };
  }

  async writeFromStream(
    path: string,
    body: AsyncIterable<Uint8Array>,
    options: { append: boolean; signal?: AbortSignal },
  ): Promise<void> {
    if (options.signal?.aborted) throw abortError();
    const command =
      `mkdir -p ${shellQuote(posix.dirname(path))} && ` +
      `cat ${options.append ? '>>' : '>'} ${shellQuote(path)}`;
    const child = spawn(this.#ssh, [this.#target, command], {
      stdio: ['pipe', 'ignore', 'pipe'],
    });
    if (!child.stdin) throw new DownloadError('SSH process has no stdin');
    let stderr = '';
    child.stderr?.on('data', (chunk: Buffer) => {
      stderr += chunk.toString();
    });
    child.stdin.on('error', (error) => {
      stderr += error.message;
    });

    const onAbort = () => child.kill('SIGTERM');
    options.signal?.addEventListener('abort', onAbort, { once: true });
    const closed = new Promise<number>((resolve, reject) => {
      child.on('error', reject);
      child.on('close', (code) => resolve(code ?? 1));
    });

    try {
      for await (const chunk of body) {
        if (options.signal?.aborted) throw abortError();
        if (!child.stdin.write(chunk)) {
          await once(child.stdin, 'drain');
        }
      }
      child.stdin.end();
      const code = await closed;
      if (options.signal?.aborted) throw abortError();
      if (code !== 0) {
        throw new DownloadError(`SSH write to ${path} failed: ${stderr.trim()}`);
      }
    } catch (error) {
      child.stdin.destroy();
      if (!child.killed) child.kill('SIGTERM');
      await closed.catch(() => undefined);
      throw error;
    } finally {
      options.signal?.removeEventListener('abort', onAbort);
    }
  }

  async rename(from: string, to: string): Promise<void> {
    const result = await this.run(`mv -f ${shellQuote(from)} ${shellQuote(to)}`);
    if (result.code !== 0) {
      throw new DownloadError(`Cannot rename ${from} over SSH: ${result.stderr.trim()}`);
    }
  }

  async remove(path: string): Promise<void> {
    const result = await this.run(`rm -f ${shellQuote(path)}`);
    if (result.code !== 0) {
      throw new DownloadError(`Cannot remove ${path} over SSH: ${result.stderr.trim()}`);
    }
  }

  async sha256(path: string): Promise<string> {
    const quoted = shellQuote(path);
    const result = await this.run(
      `if command -v sha256sum >/dev/null 2>&1; then sha256sum ${quoted}; else shasum -a 256 ${quoted}; fi`,
    );
    if (result.code !== 0) {
      throw new DownloadError(`Cannot hash ${path} over SSH: ${result.stderr.trim()}`);
    }
    const match = /\b([0-9a-f]{64})\b/i.exec(result.stdout);
    if (!match?.[1]) {
      throw new DownloadError(`Unexpected SHA-256 output for ${path}.`);
    }
    return match[1].toLowerCase();
  }

  async readText(path: string): Promise<string | null> {
    const quoted = shellQuote(path);
    const result = await this.run(
      `if [ -f ${quoted} ]; then cat ${quoted}; else exit 3; fi`,
    );
    if (result.code === 3) return null;
    if (result.code !== 0) {
      throw new DownloadError(`Cannot read ${path} over SSH: ${result.stderr.trim()}`);
    }
    return result.stdout;
  }

  async writeText(path: string, content: string): Promise<void> {
    await this.writeFromStream(
      path,
      (async function* () {
        yield Buffer.from(content, 'utf8');
      })(),
      { append: false },
    );
  }

  async run(command: string, options: { signal?: AbortSignal } = {}): Promise<CommandResult> {
    return runProcess(this.#ssh, [this.#target, command], options);
  }
}

async function runProcess(
  command: string,
  args: string[],
  options: { signal?: AbortSignal },
): Promise<CommandResult> {
  const child = spawn(command, args, { stdio: ['ignore', 'pipe', 'pipe'] });
  let stdout = '';
  let stderr = '';
  child.stdout?.on('data', (chunk: Buffer) => {
    stdout += chunk.toString();
  });
  child.stderr?.on('data', (chunk: Buffer) => {
    stderr += chunk.toString();
  });
  const onAbort = () => child.kill('SIGTERM');
  options.signal?.addEventListener('abort', onAbort, { once: true });
  try {
    const code = await new Promise<number>((resolve, reject) => {
      child.on('error', reject);
      child.on('close', (value) => resolve(value ?? 1));
    });
    if (options.signal?.aborted) throw abortError();
    return { code, stdout, stderr };
  } finally {
    options.signal?.removeEventListener('abort', onAbort);
  }
}

async function expectedSha(
  file: DownloadFile,
  fetchImpl: typeof fetch,
  headers: Record<string, string>,
  signal: AbortSignal | undefined,
): Promise<string | undefined> {
  if (file.sha256) return file.sha256;
  const init: RequestInit = { method: 'HEAD', headers };
  if (signal) init.signal = signal;
  let response: Response;
  try {
    response = await fetchImpl(file.url, init);
  } catch {
    return undefined;
  }
  if (response.status === 401 || response.status === 403) {
    throw new HubAuthError(
      'The Hub refused the download. Set hubToken (HFDOWNLOADER_HUB_TOKEN) for gated repositories.',
    );
  }
  if (!response.ok) return undefined;
  const tag = response.headers.get('x-linked-etag') ?? response.headers.get('etag');
  if (!tag) return undefined;
  const cleaned = tag.replace(/^W\//, '').replace(/^"|"$/g, '');
  return /^[0-9a-f]{64}$/i.test(cleaned) ? cleaned.toLowerCase() : undefined;
}

export async function downloadQuant(options: DownloadOptions): Promise<DownloadResult> {
  const { files, target, directory, overwrite } = options;
  const fetchImpl = options.fetch ?? globalThis.fetch;
  const now = options.now ?? Date.now;
  const signal = options.signal;

  if (files.length === 0) {
    throw new DownloadError('No files selected for download.');
  }

  const targetPaths = new Set<string>();
  const preexisting = new Set<string>();
  const existing: string[] = [];
  for (const file of files) {
    const name = file.path.split('/').pop() ?? file.path;
    const path = joinRemote(directory, name);
    if (targetPaths.has(path)) {
      throw new DownloadError(`Two selected files map to the same target: ${path}`);
    }
    targetPaths.add(path);
    if ((await target.stat(path)) !== null) {
      preexisting.add(path);
      existing.push(path);
    }
  }
  if (existing.length > 0 && !overwrite) {
    throw new OverwriteRequiredError(existing);
  }

  const total = files.reduce((sum, file) => sum + file.size, 0);
  const headers: Record<string, string> = {
    accept: 'application/octet-stream',
    'accept-encoding': 'identity',
    'user-agent': 'hfdownloader/0.1',
    ...(options.headers ?? {}),
  };
  const startedAt = now();
  let completedBytes = 0;
  let currentBytes = 0;
  let completedFiles = 0;
  let currentFile = '';
  let lastEmit = 0;
  const created: string[] = [];

  const emit = (force: boolean): void => {
    if (!options.onProgress) return;
    const timestamp = now();
    if (!force && timestamp - lastEmit < 100) return;
    lastEmit = timestamp;
    const transferred = completedBytes + currentBytes;
    const elapsed = Math.max((timestamp - startedAt) / 1000, 0.001);
    const speed = transferred / elapsed;
    const etaSeconds =
      transferred >= total ? 0 : speed > 0 ? (total - transferred) / speed : Number.POSITIVE_INFINITY;
    options.onProgress({
      file: currentFile,
      transferred,
      total,
      speed,
      etaSeconds,
      completedFiles,
      totalFiles: files.length,
    });
  };

  const cleanup = async (): Promise<void> => {
    for (const path of created) {
      if (!preexisting.has(path)) {
        await target.remove(path).catch(() => undefined);
      }
    }
  };

  try {
    for (const file of files) {
      if (signal?.aborted) throw abortError();
      const name = file.path.split('/').pop() ?? file.path;
      const targetPath = joinRemote(directory, name);
      const partPath = `${targetPath}.part`;
      currentBytes = 0;
      currentFile = name;

      const expected = await expectedSha(file, fetchImpl, headers, signal);
      let partSize = (await target.stat(partPath))?.size ?? 0;
      if (partSize > file.size) {
        await target.remove(partPath);
        partSize = 0;
      }

      if (partSize < file.size || file.size === 0) {
        const requestHeaders: Record<string, string> = { ...headers };
        if (partSize > 0) requestHeaders.range = `bytes=${partSize}-`;
        const init: RequestInit = { headers: requestHeaders };
        if (signal) init.signal = signal;
        const response = await fetchImpl(file.url, init);

        if (response.status === 401 || response.status === 403) {
          throw new HubAuthError(
            'The Hub refused the download. Set hubToken (HFDOWNLOADER_HUB_TOKEN) for gated repositories.',
          );
        }
        if (response.status === 416 && partSize === file.size) {
          // The partial file is already complete; verify and promote it below.
        } else if (!response.ok) {
          throw new DownloadError(
            `The Hub returned ${response.status} while downloading ${file.path}.`,
          );
        } else if (response.status === 206) {
          const contentRange = response.headers.get('content-range');
          const match = contentRange ? /^bytes (\d+)-/.exec(contentRange) : null;
          if (match && Number(match[1]) !== partSize) {
            partSize = 0;
          }
          if (partSize === 0) {
            await target.remove(partPath);
          }
          if (response.body) {
            const body = response.body as unknown as AsyncIterable<Uint8Array>;
            await target.writeFromStream(partPath, countChunks(body, (n) => {
              currentBytes = partSize + n;
              emit(false);
            }, partSize), {
              append: partSize > 0,
              ...(signal ? { signal } : {}),
            });
          }
        } else if (response.status === 200) {
          partSize = 0;
          await target.remove(partPath);
          if (response.body) {
            const body = response.body as unknown as AsyncIterable<Uint8Array>;
            await target.writeFromStream(partPath, countChunks(body, (n) => {
              currentBytes = n;
              emit(false);
            }, 0), {
              append: false,
              ...(signal ? { signal } : {}),
            });
          }
        }
      } else {
        currentBytes = partSize;
        emit(true);
      }

      if (signal?.aborted) throw abortError();
      const actualSize = (await target.stat(partPath))?.size ?? 0;
      if (actualSize !== file.size) {
        await target.remove(partPath);
        throw new DownloadError(
          `Downloaded ${file.path} is ${actualSize} bytes, expected ${file.size}.`,
        );
      }

      if (expected) {
        const actual = await target.sha256(partPath);
        if (actual.toLowerCase() !== expected.toLowerCase()) {
          await target.remove(partPath);
          throw new ChecksumError(targetPath, expected, actual);
        }
      }

      await target.rename(partPath, targetPath);
      created.push(targetPath);
      completedBytes += file.size;
      currentBytes = 0;
      completedFiles += 1;
      emit(true);
    }
  } catch (error) {
    const currentPath = files[completedFiles]
      ? joinRemote(directory, files[completedFiles]!.path.split('/').pop() ?? '')
      : undefined;
    if (currentPath) {
      await target.remove(`${currentPath}.part`).catch(() => undefined);
    }
    await cleanup();
    if (signal?.aborted && (error as Error).name !== 'AbortError') {
      throw abortError();
    }
    throw error;
  }

  return {
    files: files.map((file) => ({
      path: joinRemote(directory, file.path.split('/').pop() ?? file.path),
      size: file.size,
    })),
    bytes: total,
  };
}

async function* countChunks(
  body: AsyncIterable<Uint8Array>,
  onChunk: (bytes: number) => void,
  initial: number,
): AsyncGenerator<Uint8Array> {
  let seen = initial;
  for await (const chunk of body) {
    seen += chunk.byteLength;
    onChunk(seen - initial);
    yield chunk;
  }
}
