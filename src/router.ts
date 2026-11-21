import type { Quant } from './hub.ts';

export interface RouterClientOptions {
  baseUrl: string;
  apiKey?: string;
  fetch?: typeof fetch;
}

export interface ContextSuggestionInput {
  repo: string;
  quant: Quant;
  helperModel: string;
  fallback?: number | undefined;
  timeoutMs?: number;
  signal?: AbortSignal | undefined;
}

export interface ContextSuggestion {
  value: number | null;
  source: 'helper' | 'hub' | 'none';
}

export interface RouterPreset {
  id: string;
  preset?: string;
  contextLength?: number;
}

export class RouterError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'RouterError';
  }
}

function abortError(): Error {
  const error = new Error('Request cancelled');
  error.name = 'AbortError';
  return error;
}

function parseSuggestedContext(content: string): number | null {
  const normalized = content.replace(/,/g, '');
  const thousands = /(\d+(?:\.\d+)?)\s*k\b/i.exec(normalized);
  if (thousands?.[1]) {
    const value = Math.round(Number(thousands[1]) * 1024);
    if (Number.isInteger(value) && value > 0 && value <= 10_000_000) return value;
  }
  for (const match of normalized.matchAll(/\d+/g)) {
    const value = Number(match[0]);
    if (Number.isInteger(value) && value > 0 && value <= 10_000_000) return value;
  }
  return null;
}

function promptFor(input: ContextSuggestionInput): string {
  const files = input.quant.files
    .map((file) => `${file.path} (${file.size} bytes)`)
    .join(', ');
  return [
    'Choose a safe llama.cpp context window for this GGUF model.',
    `Repository: ${input.repo}`,
    `Quant: ${input.quant.label}`,
    `Architecture: ${input.quant.architecture ?? 'unknown'}`,
    `Published Hub context limit: ${input.quant.contextLength ?? 'unknown'} tokens`,
    `Files: ${files}`,
    'Reply with one integer number of tokens and no other text.',
  ].join('\n');
}

export class RouterClient {
  readonly baseUrl: string;
  readonly apiKey?: string;
  readonly #fetch: typeof fetch;

  constructor(options: RouterClientOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    if (options.apiKey) this.apiKey = options.apiKey;
    this.#fetch = options.fetch ?? globalThis.fetch;
  }

  async suggestContextWindow(input: ContextSuggestionInput): Promise<ContextSuggestion> {
    if (input.signal?.aborted) throw abortError();
    const fallback: ContextSuggestion =
      input.fallback !== undefined
        ? { value: input.fallback, source: 'hub' }
        : { value: null, source: 'none' };

    const controller = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, input.timeoutMs ?? 15_000);
    const onAbort = (): void => controller.abort();
    input.signal?.addEventListener('abort', onAbort, { once: true });

    try {
      const response = await this.#fetch(`${this.baseUrl}/v1/chat/completions`, {
        method: 'POST',
        headers: this.#headers(),
        body: JSON.stringify({
          model: input.helperModel,
          messages: [
            {
              role: 'system',
              content:
                'You size context windows for llama.cpp Presets. Answer with a single integer.',
            },
            { role: 'user', content: promptFor(input) },
          ],
          temperature: 0,
          max_tokens: 32,
          stream: false,
        }),
        signal: controller.signal,
      });

      if (input.signal?.aborted) throw abortError();
      if (!response.ok) return fallback;

      const payload = (await response.json()) as {
        choices?: { message?: { content?: unknown }; text?: unknown }[];
      };
      const choice = payload.choices?.[0];
      const content =
        typeof choice?.message?.content === 'string'
          ? choice.message.content
          : typeof choice?.text === 'string'
            ? choice.text
            : '';
      const value = parseSuggestedContext(content);
      return value === null ? fallback : { value, source: 'helper' };
    } catch (error) {
      if (input.signal?.aborted) throw abortError();
      if (timedOut) return fallback;
      if ((error as Error).name === 'AbortError') return fallback;
      return fallback;
    } finally {
      clearTimeout(timer);
      input.signal?.removeEventListener('abort', onAbort);
    }
  }

  async listPresets(): Promise<RouterPreset[]> {
    const response = await this.#fetch(`${this.baseUrl}/v1/models`, {
      headers: this.#headers(),
    });
    if (!response.ok) {
      throw new RouterError(`The Router returned ${response.status} while listing models.`);
    }
    const payload = (await response.json()) as {
      data?: {
        id?: unknown;
        preset?: unknown;
        context_length?: unknown;
      }[];
    };
    const presets: RouterPreset[] = [];
    for (const entry of payload.data ?? []) {
      if (typeof entry.id !== 'string') continue;
      const preset: RouterPreset = { id: entry.id };
      if (typeof entry.preset === 'string') preset.preset = entry.preset;
      if (typeof entry.context_length === 'number') preset.contextLength = entry.context_length;
      presets.push(preset);
    }
    return presets;
  }

  async isPresetVisible(alias: string): Promise<boolean> {
    const presets = await this.listPresets();
    return presets.some((preset) => preset.id === alias);
  }

  async waitForPreset(
    alias: string,
    options: { timeoutMs?: number; intervalMs?: number } = {},
  ): Promise<boolean> {
    const timeoutMs = options.timeoutMs ?? 3_000;
    const intervalMs = options.intervalMs ?? 250;
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      try {
        if (await this.isPresetVisible(alias)) return true;
      } catch {
        // A transient Router failure should not make a successful write look lost.
      }
      if (Date.now() >= deadline) return false;
      await new Promise((resolve) => setTimeout(resolve, intervalMs));
    }
  }

  #headers(): Record<string, string> {
    const headers: Record<string, string> = {
      accept: 'application/json',
      'content-type': 'application/json',
      'user-agent': 'hfdownloader/0.1',
    };
    if (this.apiKey) headers.authorization = `Bearer ${this.apiKey}`;
    return headers;
  }
}
