export interface ParsedHubUrl {
  repo: string;
  revision: string;
}

export interface HubSidecar {
  path: string;
  size: number;
  sha256?: string;
  kind: 'mmproj' | 'mtp';
}

export interface HubFile {
  path: string;
  size: number;
  sha256?: string;
  shard?: { index: number; total: number };
}

export interface Quant {
  label: string;
  files: HubFile[];
  size: number;
  sidecars: HubSidecar[];
  contextLength?: number;
  architecture?: string;
}

export interface RepoInfo {
  repo: string;
  revision: string;
  quants: Quant[];
  sidecars: HubSidecar[];
  contextLength?: number;
  architecture?: string;
}

export interface HubClientOptions {
  baseUrl?: string;
  token?: string;
  fetch?: typeof fetch;
}

export class HubError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'HubError';
  }
}

export class HubAuthError extends HubError {
  constructor(message: string) {
    super(message);
    this.name = 'HubAuthError';
  }
}

interface TreeEntry {
  type?: string;
  path?: string;
  size?: number;
  oid?: string;
  lfs?: { oid?: string; size?: number };
  gguf?: { context_length?: number; architecture?: string };
}

const SHARD_PATTERN = /^(.*)-(\d{5})-of-(\d{5})\.gguf$/i;
const QUANT_PATTERN =
  /(?:^|[-_.])((?:UD[-_])?(?:IQ\d+(?:_[A-Z0-9]+)*|Q\d+(?:_[A-Z0-9]+)*|BF16|F16|F32|FP16))(?:[-_.]|$)/i;
const SHA256_PATTERN = /^[0-9a-f]{64}$/i;

function isSha256(value: unknown): value is string {
  return typeof value === 'string' && SHA256_PATTERN.test(value);
}

export function parseHubUrl(input: string): ParsedHubUrl {
  const trimmed = input.trim();
  if (trimmed === '') {
    throw new HubError('Enter a Hugging Face repository URL.');
  }

  let url: URL;
  if (/^https?:\/\//i.test(trimmed)) {
    try {
      url = new URL(trimmed);
    } catch {
      throw new HubError(`Not a valid URL: ${trimmed}`);
    }
    if (url.hostname !== 'huggingface.co' && url.hostname !== 'www.huggingface.co') {
      throw new HubError('Only huggingface.co URLs are supported.');
    }
  } else {
    const repo = trimmed.replace(/^\/+|\/+$/g, '');
    const parts = repo.split('/');
    if (parts.length !== 2 || parts.some((part) => part === '')) {
      throw new HubError('Enter a URL like https://huggingface.co/org/repo.');
    }
    return { repo, revision: 'main' };
  }

  const parts = url.pathname.split('/').filter(Boolean);
  if (parts.length < 2) {
    throw new HubError('The URL must contain an organization and a repository name.');
  }
  const repo = `${parts[0]}/${parts[1]}`;
  let revision = 'main';
  const marker = parts[2];
  if (marker === 'tree' || marker === 'blob') {
    revision = parts[3] ? decodeURIComponent(parts[3]) : 'main';
  }
  return { repo, revision };
}

function quantLabelFromStem(stem: string): string {
  const match = QUANT_PATTERN.exec(stem);
  if (!match?.[1]) {
    return stem;
  }
  return match[1].toUpperCase();
}

function sidecarKind(path: string): HubSidecar['kind'] | undefined {
  const base = path.split('/').pop() ?? path;
  if (/(^|[-_.])mmproj([-_.]|$)/i.test(base)) return 'mmproj';
  if (/(^|[-_.])mtp([-_.]|$)/i.test(base)) return 'mtp';
  return undefined;
}

function shaFromEntry(entry: TreeEntry): string | undefined {
  if (isSha256(entry.lfs?.oid)) return entry.lfs.oid;
  if (isSha256(entry.oid)) return entry.oid;
  return undefined;
}

function linkNext(header: string | null): string | undefined {
  if (!header) return undefined;
  for (const part of header.split(',')) {
    const match = /<([^>]+)>\s*;\s*rel="?next"?/.exec(part);
    if (match?.[1]) return match[1];
  }
  return undefined;
}

export class HubClient {
  readonly baseUrl: string;
  readonly token?: string;
  readonly #fetch: typeof fetch;

  constructor(options: HubClientOptions = {}) {
    this.baseUrl = (options.baseUrl ?? 'https://huggingface.co').replace(/\/+$/, '');
    if (options.token) this.token = options.token;
    this.#fetch = options.fetch ?? globalThis.fetch;
  }

  resolveUrl(repo: string, path: string, revision = 'main'): string {
    const encodedRepo = repo
      .split('/')
      .map((segment) => encodeURIComponent(segment))
      .join('/');
    const encodedPath = path
      .split('/')
      .map((segment) => encodeURIComponent(segment))
      .join('/');
    return `${this.baseUrl}/${encodedRepo}/resolve/${encodeURIComponent(revision)}/${encodedPath}?download=true`;
  }

  async listQuants(repo: string, revision = 'main'): Promise<RepoInfo> {
    const [entries, info] = await Promise.all([
      this.#fetchTree(repo, revision),
      this.#fetchModelInfo(repo),
    ]);
    return buildRepoInfo(repo, revision, entries, info);
  }

  async headSha256(url: string, signal?: AbortSignal): Promise<string | undefined> {
    const headers = this.#headers();
    const init: RequestInit = { method: 'HEAD', headers };
    if (signal) init.signal = signal;
    const response = await this.#fetch(url, init);
    if (!response.ok) return undefined;
    const linked = response.headers.get('x-linked-etag');
    const etag = linked ?? response.headers.get('etag');
    if (!etag) return undefined;
    const cleaned = etag.replace(/^W\//, '').replace(/^"|"$/g, '');
    return isSha256(cleaned) ? cleaned : undefined;
  }

  #headers(): Record<string, string> {
    const headers: Record<string, string> = {
      accept: 'application/json',
      'user-agent': 'hfdownloader/0.1',
    };
    if (this.token) headers.authorization = `Bearer ${this.token}`;
    return headers;
  }

  async #fetchTree(repo: string, revision: string): Promise<TreeEntry[]> {
    const entries: TreeEntry[] = [];
    let url: string | undefined =
      `${this.baseUrl}/api/models/${encodeRepo(repo)}/tree/${encodeURIComponent(revision)}?recursive=true`;
    while (url) {
      const response = await this.#fetch(url, { headers: this.#headers() });
      if (response.status === 401 || response.status === 403) {
        throw new HubAuthError(
          'The Hub refused access to this repository. Set hubToken (HFDOWNLOADER_HUB_TOKEN) to browse gated or private repositories.',
        );
      }
      if (!response.ok) {
        throw new HubError(
          `The Hub returned ${response.status} for ${repo}. Check the repository URL.`,
        );
      }
      const payload = (await response.json()) as unknown;
      if (!Array.isArray(payload)) {
        throw new HubError(`Unexpected Hub response while listing ${repo}.`);
      }
      for (const entry of payload) {
        if (entry && typeof entry === 'object') entries.push(entry as TreeEntry);
      }
      const next = linkNext(response.headers.get('link'));
      url = next ? new URL(next, this.baseUrl).toString() : undefined;
    }
    return entries;
  }

  async #fetchModelInfo(repo: string): Promise<Record<string, unknown> | undefined> {
    const response = await this.#fetch(
      `${this.baseUrl}/api/models/${encodeRepo(repo)}?blobs=true`,
      { headers: this.#headers() },
    );
    if (response.status === 401 || response.status === 403) {
      throw new HubAuthError(
        'The Hub refused access to this repository. Set hubToken (HFDOWNLOADER_HUB_TOKEN) to browse gated or private repositories.',
      );
    }
    if (!response.ok) return undefined;
    const payload = (await response.json()) as unknown;
    return payload && typeof payload === 'object' && !Array.isArray(payload)
      ? (payload as Record<string, unknown>)
      : undefined;
  }
}

function encodeRepo(repo: string): string {
  return repo
    .split('/')
    .map((segment) => encodeURIComponent(segment))
    .join('/');
}

function buildRepoInfo(
  repo: string,
  revision: string,
  entries: TreeEntry[],
  info: Record<string, unknown> | undefined,
): RepoInfo {
  const gguf = info?.gguf;
  const repoGguf =
    gguf && typeof gguf === 'object' && !Array.isArray(gguf)
      ? (gguf as Record<string, unknown>)
      : undefined;
  const contextLength =
    typeof repoGguf?.context_length === 'number' ? repoGguf.context_length : undefined;
  const architecture =
    typeof repoGguf?.architecture === 'string' ? repoGguf.architecture : undefined;

  const sidecars: HubSidecar[] = [];
  const groups = new Map<
    string,
    { label: string; files: HubFile[]; contextLength?: number; architecture?: string }
  >();

  for (const entry of entries) {
    if (entry.type !== 'file' || typeof entry.path !== 'string') continue;
    if (!entry.path.toLowerCase().endsWith('.gguf')) continue;

    const size = typeof entry.size === 'number' ? entry.size : (entry.lfs?.size ?? 0);
    const sha256 = shaFromEntry(entry);
    const kind = sidecarKind(entry.path);
    if (kind) {
      const sidecar: HubSidecar = { path: entry.path, size, kind };
      if (sha256) sidecar.sha256 = sha256;
      sidecars.push(sidecar);
      continue;
    }

    const base = entry.path.split('/').pop() ?? entry.path;
    const shard = SHARD_PATTERN.exec(base);
    const stem = shard?.[1] ?? base.replace(/\.gguf$/i, '');
    const key = shard ? `shard:${stem}` : `file:${stem}`;
    const label = quantLabelFromStem(stem);

    let group = groups.get(key);
    if (!group) {
      group = { label, files: [] };
      const entryContext = entry.gguf?.context_length;
      const entryArch = entry.gguf?.architecture;
      if (typeof entryContext === 'number') group.contextLength = entryContext;
      else if (contextLength !== undefined) group.contextLength = contextLength;
      if (typeof entryArch === 'string') group.architecture = entryArch;
      else if (architecture !== undefined) group.architecture = architecture;
      groups.set(key, group);
    }

    const file: HubFile = { path: entry.path, size };
    if (sha256) file.sha256 = sha256;
    if (shard) {
      file.shard = { index: Number(shard[2]), total: Number(shard[3]) };
    }
    group.files.push(file);
  }

  const quants: Quant[] = [];
  for (const group of groups.values()) {
    group.files.sort((a, b) => (a.shard?.index ?? 0) - (b.shard?.index ?? 0));
    const quant: Quant = {
      label: group.label,
      files: group.files,
      size: group.files.reduce((sum, file) => sum + file.size, 0),
      sidecars: sidecars.slice(),
    };
    if (group.contextLength !== undefined) quant.contextLength = group.contextLength;
    if (group.architecture !== undefined) quant.architecture = group.architecture;
    quants.push(quant);
  }

  const result: RepoInfo = { repo, revision, quants, sidecars };
  if (contextLength !== undefined) result.contextLength = contextLength;
  if (architecture !== undefined) result.architecture = architecture;
  return result;
}
