import type { Config } from './config.ts';
import type { HubSidecar, Quant, RepoInfo } from './hub.ts';
import { parseHubUrl } from './hub.ts';
import {
  applyRawPreset,
  copyDefaultsFromPreset,
  createPresetDraft,
  PresetError,
  renderPreset,
  writePreset,
  type PresetDraft,
} from './preset.ts';
import type { ContextSuggestion, RouterPreset } from './router.ts';
import {
  ChecksumError,
  downloadQuant,
  OverwriteRequiredError,
  type DownloadFile,
  type DownloadProgress,
  type Target,
} from './transfer.ts';

export interface HubLike {
  listQuants(repo: string, revision?: string): Promise<RepoInfo>;
  resolveUrl(repo: string, path: string, revision?: string): string;
}

export interface RouterLike {
  suggestContextWindow(input: {
    repo: string;
    quant: Quant;
    helperModel: string;
    fallback?: number | undefined;
    timeoutMs?: number;
    signal?: AbortSignal | undefined;
  }): Promise<ContextSuggestion>;
  waitForPreset(
    alias: string,
    options?: { timeoutMs?: number; intervalMs?: number },
  ): Promise<boolean>;
}

export type DownloadFn = typeof downloadQuant;

export type Screen =
  | 'url'
  | 'loading-repo'
  | 'picker'
  | 'overwrite'
  | 'downloading'
  | 'preset'
  | 'alias-confirm'
  | 'restart'
  | 'restarting'
  | 'done'
  | 'error';

export type RetryKind = 'repo' | 'download' | 'preset' | 'restart';

export interface PresetUiState {
  draft: PresetDraft;
  raw: string;
  rawMode: boolean;
  field: number;
  editing: boolean;
  contextInput: string;
  contextSource: 'helper' | 'hub' | 'none';
  suggestionPending: boolean;
  contextEdited: boolean;
  defaultsSource?: string;
  aliasConflict?: string;
  visible?: boolean;
  backupPath?: string;
}

export interface SessionState {
  screen: Screen;
  urlInput: string;
  repo?: RepoInfo;
  selectedQuant: number;
  pickerFocus: 'quants' | 'sidecars';
  sidecarCursor: number;
  selectedSidecars: string[];
  overwritePaths: string[];
  progress?: DownloadProgress;
  preset?: PresetUiState;
  restartCommand: string;
  error?: { message: string; retry: RetryKind };
  notice?: { kind: 'info' | 'error'; message: string };
  done?: { alias: string; modelPath: string; backupPath?: string };
}

export interface SessionOptions {
  config: Config;
  hub: HubLike;
  target: Target;
  router: RouterLike;
  initialUrl?: string;
  download?: DownloadFn;
  now?: () => Date;
}

export class Session {
  readonly config: Config;
  readonly hub: HubLike;
  readonly target: Target;
  readonly router: RouterLike;
  readonly #download: DownloadFn;
  readonly #now: () => Date;
  #state: SessionState;
  #listeners = new Set<(state: SessionState) => void>();
  #downloadAbort?: AbortController;
  #aliasConfirmed = false;
  #closed = false;
  #pending = new Set<Promise<unknown>>();

  constructor(options: SessionOptions) {
    this.config = options.config;
    this.hub = options.hub;
    this.target = options.target;
    this.router = options.router;
    this.#download = options.download ?? downloadQuant;
    this.#now = options.now ?? (() => new Date());
    this.#state = {
      screen: 'url',
      urlInput: '',
      selectedQuant: 0,
      pickerFocus: 'quants',
      sidecarCursor: 0,
      selectedSidecars: [],
      overwritePaths: [],
      restartCommand: options.config.restartCommand,
    };
    if (options.initialUrl) this.#state.urlInput = options.initialUrl;
  }

  get state(): SessionState {
    return this.#state;
  }

  get closed(): boolean {
    return this.#closed;
  }

  subscribe(listener: (state: SessionState) => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  track<T>(promise: Promise<T>): Promise<T> {
    this.#pending.add(promise);
    void promise.finally(() => this.#pending.delete(promise));
    return promise;
  }

  async waitForIdle(): Promise<void> {
    while (this.#pending.size > 0) {
      await Promise.allSettled([...this.#pending]);
    }
  }

  async start(): Promise<void> {
    const initial = this.#state.urlInput;
    if (initial) {
      await this.openRepo(initial);
    } else {
      this.#emit();
    }
  }

  setUrlInput(value: string): void {
    this.#state = { ...this.#state, urlInput: value };
    this.#emit();
  }

  async submitUrl(input?: string): Promise<void> {
    const value = (input ?? this.#state.urlInput).trim();
    this.#state = { ...this.#state, urlInput: value };
    await this.openRepo(value);
  }

  async openRepo(input: string): Promise<void> {
    let parsed;
    try {
      parsed = parseHubUrl(input);
    } catch (error) {
      this.#fail((error as Error).message, 'repo');
      return;
    }
    this.#state = {
      ...this.#state,
      screen: 'loading-repo',
      urlInput: input,
      notice: undefined,
      error: undefined,
    };
    this.#emit();
    try {
      const repo = await this.hub.listQuants(parsed.repo, parsed.revision);
      if (this.#closed) return;
      if (repo.quants.length === 0) {
        this.#fail('No GGUF Quants were found in that repository.', 'repo');
        return;
      }
      this.#state = {
        ...this.#state,
        screen: 'picker',
        repo,
        selectedQuant: 0,
        pickerFocus: 'quants',
        sidecarCursor: 0,
        selectedSidecars: [],
        notice: undefined,
        error: undefined,
      };
      this.#emit();
    } catch (error) {
      if (this.#closed) return;
      this.#fail((error as Error).message, 'repo');
    }
  }

  selectedQuant(): Quant | undefined {
    return this.#state.repo?.quants[this.#state.selectedQuant];
  }

  moveSelection(delta: number): void {
    const state = this.#state;
    if (state.screen === 'picker') {
      if (state.pickerFocus === 'quants') {
        const count = state.repo?.quants.length ?? 0;
        if (count === 0) return;
        const selected = (state.selectedQuant + delta + count) % count;
        this.#state = { ...state, selectedQuant: selected };
      } else {
        const count = state.repo?.sidecars.length ?? 0;
        if (count === 0) return;
        const cursor = (state.sidecarCursor + delta + count) % count;
        this.#state = { ...state, sidecarCursor: cursor };
      }
      this.#emit();
    } else if (state.screen === 'preset' && state.preset && !state.preset.rawMode) {
      const field = (state.preset.field + delta + 3) % 3;
      this.#state = {
        ...state,
        preset: { ...state.preset, field, editing: false },
      };
      this.#emit();
    }
  }

  setPickerFocus(focus: 'quants' | 'sidecars'): void {
    if (this.#state.screen !== 'picker') return;
    this.#state = { ...this.#state, pickerFocus: focus };
    this.#emit();
  }

  togglePickerFocus(): void {
    this.setPickerFocus(this.#state.pickerFocus === 'quants' ? 'sidecars' : 'quants');
  }

  toggleSidecar(path?: string): void {
    const state = this.#state;
    if (state.screen !== 'picker' || !state.repo) return;
    const sidecarPath = path ?? state.repo.sidecars[state.sidecarCursor]?.path;
    if (!sidecarPath) return;
    const selected = state.selectedSidecars.includes(sidecarPath)
      ? state.selectedSidecars.filter((value) => value !== sidecarPath)
      : [...state.selectedSidecars, sidecarPath];
    this.#state = { ...state, selectedSidecars: selected };
    this.#emit();
  }

  async confirmQuant(): Promise<void> {
    if (this.#state.screen !== 'picker') return;
    const quant = this.selectedQuant();
    const repo = this.#state.repo;
    if (!quant || !repo) return;
    const sidecars = repo.sidecars.filter((sidecar) =>
      this.#state.selectedSidecars.includes(sidecar.path),
    );
    const files: DownloadFile[] = [...quant.files, ...sidecars].map((file) => ({
      path: file.path,
      size: file.size,
      ...(file.sha256 ? { sha256: file.sha256 } : {}),
      url: this.hub.resolveUrl(repo.repo, file.path, repo.revision),
    }));
    await this.#runDownload(files, quant, sidecars, false);
  }

  async confirmOverwrite(): Promise<void> {
    if (this.#state.screen !== 'overwrite') return;
    const quant = this.selectedQuant();
    const repo = this.#state.repo;
    if (!quant || !repo) return;
    const sidecars = repo.sidecars.filter((sidecar) =>
      this.#state.selectedSidecars.includes(sidecar.path),
    );
    const files: DownloadFile[] = [...quant.files, ...sidecars].map((file) => ({
      path: file.path,
      size: file.size,
      ...(file.sha256 ? { sha256: file.sha256 } : {}),
      url: this.hub.resolveUrl(repo.repo, file.path, repo.revision),
    }));
    await this.#runDownload(files, quant, sidecars, true);
  }

  cancelOverwrite(): void {
    if (this.#state.screen !== 'overwrite') return;
    this.#state = {
      ...this.#state,
      screen: 'picker',
      overwritePaths: [],
      notice: { kind: 'info', message: 'Download cancelled.' },
    };
    this.#emit();
  }

  cancelDownload(): void {
    this.#downloadAbort?.abort();
  }

  async #runDownload(
    files: DownloadFile[],
    quant: Quant,
    sidecars: HubSidecar[],
    overwrite: boolean,
  ): Promise<void> {
    const repo = this.#state.repo;
    if (!repo) return;
    const controller = new AbortController();
    this.#downloadAbort = controller;
    this.#state = {
      ...this.#state,
      screen: 'downloading',
      overwritePaths: [],
      notice: undefined,
      error: undefined,
      progress: {
        file: files[0]?.path ?? '',
        transferred: 0,
        total: files.reduce((sum, file) => sum + file.size, 0),
        speed: 0,
        etaSeconds: 0,
        completedFiles: 0,
        totalFiles: files.length,
      },
    };
    this.#emit();

    try {
      const headers: Record<string, string> = {};
      if (this.config.hubToken) headers.authorization = `Bearer ${this.config.hubToken}`;
      const result = await this.#download({
        files,
        target: this.target,
        directory: this.config.ggufDirectory,
        overwrite,
        signal: controller.signal,
        ...(Object.keys(headers).length > 0 ? { headers } : {}),
        onProgress: (progress) => {
          if (this.#closed) return;
          this.#state = { ...this.#state, progress };
          this.#emit();
        },
      });
      if (this.#closed) return;

      const mainPath = result.files[0]?.path;
      if (!mainPath) {
        this.#fail('The download finished without a model file.', 'download');
        return;
      }
      const sidecarPaths = new Map(
        result.files.map((file) => [file.path.split('/').pop() ?? file.path, file.path]),
      );
      const mmproj = sidecars.find((sidecar) => sidecar.kind === 'mmproj');
      const mtp = sidecars.find((sidecar) => sidecar.kind === 'mtp');
      const mmprojPath = mmproj
        ? sidecarPaths.get(mmproj.path.split('/').pop() ?? mmproj.path)
        : undefined;
      const mtpPath = mtp
        ? sidecarPaths.get(mtp.path.split('/').pop() ?? mtp.path)
        : undefined;
      let existingPresetText: string | undefined;
      try {
        existingPresetText = (await this.target.readText(this.config.presetPath)) ?? undefined;
      } catch {
        existingPresetText = undefined;
      }
      const fallback = quant.contextLength ?? repo.contextLength;
      const draft = createPresetDraft({
        repo: repo.repo,
        quantLabel: quant.label,
        modelPath: mainPath,
        ...(fallback !== undefined ? { contextWindow: fallback } : {}),
        ...(mmprojPath ? { mmprojPath } : {}),
        ...(mtpPath ? { mtpPath } : {}),
        ...(existingPresetText ? { existingPresetText } : {}),
      });
      const preset: PresetUiState = {
        draft,
        raw: renderPreset(draft),
        rawMode: false,
        field: 0,
        editing: false,
        contextInput: draft.contextWindow === null ? '' : String(draft.contextWindow),
        contextSource: fallback !== undefined ? 'hub' : 'none',
        suggestionPending: true,
        contextEdited: false,
      };
      if (existingPresetText) {
        const first = existingPresetText.match(/^\s*\[([^\]]+)\]/m);
        if (first?.[1]) preset.defaultsSource = first[1].trim();
      }
      this.#state = {
        ...this.#state,
        screen: 'preset',
        preset,
        progress: undefined,
        notice: {
          kind: 'info',
          message: 'Asking the helper model for a Context Window…',
        },
      };
      this.#emit();

      try {
        const suggestion = await this.router.suggestContextWindow({
          repo: repo.repo,
          quant,
          helperModel: this.config.helperModel,
          ...(fallback !== undefined ? { fallback } : {}),
          timeoutMs: this.config.helperTimeoutMs,
        });
        if (this.#closed || this.#state.screen !== 'preset' || !this.#state.preset) return;
        const current = this.#state.preset;
        const value = suggestion.value ?? fallback ?? null;
        const next: PresetUiState = {
          ...current,
          suggestionPending: false,
          contextSource: suggestion.source,
        };
        if (!current.contextEdited && !current.rawMode && value !== null) {
          next.draft = { ...current.draft, contextWindow: value };
          next.contextInput = String(value);
          next.raw = renderPreset(next.draft);
        }
        this.#state = {
          ...this.#state,
          preset: next,
          notice: {
            kind: 'info',
            message:
              suggestion.source === 'helper'
                ? 'Context Window suggested by the helper model. Review it before writing.'
                : 'Helper unavailable; using the published Hub context limit. Edit it if needed.',
          },
        };
        this.#emit();
      } catch (error) {
        if (this.#closed) return;
        if (this.#state.screen === 'preset' && this.#state.preset) {
          this.#state = {
            ...this.#state,
            preset: { ...this.#state.preset, suggestionPending: false },
            notice: {
              kind: 'info',
              message: `Context suggestion unavailable (${(error as Error).message}). Using the Hub limit.`,
            },
          };
          this.#emit();
        }
      }
    } catch (error) {
      if (this.#closed) return;
      if (error instanceof OverwriteRequiredError) {
        this.#state = {
          ...this.#state,
          screen: 'overwrite',
          overwritePaths: error.paths,
          progress: undefined,
        };
        this.#emit();
        return;
      }
      if ((error as Error).name === 'AbortError') {
        this.#state = {
          ...this.#state,
          screen: 'picker',
          progress: undefined,
          notice: { kind: 'info', message: 'Download cancelled. Partial files were removed.' },
        };
        this.#emit();
        return;
      }
      if (error instanceof ChecksumError) {
        this.#fail(
          `${error.message}. The file was discarded, so no Preset was created.`,
          'download',
        );
        return;
      }
      this.#fail((error as Error).message, 'download');
    } finally {
      if (this.#downloadAbort === controller) this.#downloadAbort = undefined;
    }
  }

  // --- Preset editing -------------------------------------------------------

  setAlias(value: string): void {
    const preset = this.#state.preset;
    if (!preset) return;
    const draft = { ...preset.draft, alias: value };
    const next = { ...preset, draft, raw: preset.rawMode ? preset.raw : renderPresetSafe(draft, preset.raw) };
    this.#state = { ...this.#state, preset: next, notice: undefined };
    this.#emit();
  }

  setContextInput(value: string): void {
    const preset = this.#state.preset;
    if (!preset) return;
    this.#state = {
      ...this.#state,
      preset: { ...preset, contextInput: value, contextEdited: true, editing: true },
      notice: undefined,
    };
    this.#emit();
  }

  commitContextInput(): boolean {
    const preset = this.#state.preset;
    if (!preset) return false;
    const value = Number(preset.contextInput.trim());
    if (!Number.isInteger(value) || value <= 0) {
      this.#notice('error', 'The Context Window must be a positive whole number.');
      return false;
    }
    const draft = { ...preset.draft, contextWindow: value };
    this.#state = {
      ...this.#state,
      preset: {
        ...preset,
        draft,
        contextInput: String(value),
        contextEdited: true,
        raw: renderPreset(draft),
      },
      notice: undefined,
    };
    this.#emit();
    return true;
  }

  setContextWindow(value: number): void {
    this.setContextInput(String(value));
    this.commitContextInput();
  }

  selectPresetField(field: number): void {
    const preset = this.#state.preset;
    if (!preset || preset.rawMode) return;
    this.#state = {
      ...this.#state,
      preset: { ...preset, field: ((field % 3) + 3) % 3, editing: false },
    };
    this.#emit();
  }

  beginPresetEdit(): void {
    const preset = this.#state.preset;
    if (!preset || preset.rawMode || preset.field === 2) return;
    this.#state = { ...this.#state, preset: { ...preset, editing: true } };
    this.#emit();
  }

  endPresetEdit(): void {
    const preset = this.#state.preset;
    if (!preset || !preset.editing) return;
    if (preset.field === 1) {
      if (!this.commitContextInput()) return;
    }
    this.#state = {
      ...this.#state,
      preset: { ...this.#state.preset!, editing: false },
    };
    this.#emit();
  }

  toggleRawMode(): void {
    const preset = this.#state.preset;
    if (!preset) return;
    if (!preset.rawMode) {
      this.#state = {
        ...this.#state,
        preset: { ...preset, rawMode: true, editing: false, raw: renderPreset(preset.draft) },
        notice: undefined,
      };
      this.#emit();
      return;
    }
    try {
      const draft = applyRawPreset(preset.draft, preset.raw);
      this.#state = {
        ...this.#state,
        preset: {
          ...preset,
          draft,
          rawMode: false,
          editing: false,
          contextInput: draft.contextWindow === null ? '' : String(draft.contextWindow),
        },
        notice: undefined,
      };
      this.#emit();
    } catch (error) {
      this.#notice('error', (error as Error).message);
    }
  }

  setRawText(value: string): void {
    const preset = this.#state.preset;
    if (!preset) return;
    this.#state = {
      ...this.#state,
      preset: { ...preset, raw: value, rawMode: true },
      notice: undefined,
    };
    this.#emit();
  }

  applyRawText(): boolean {
    const preset = this.#state.preset;
    if (!preset) return false;
    try {
      const draft = applyRawPreset(preset.draft, preset.raw);
      this.#state = {
        ...this.#state,
        preset: {
          ...preset,
          draft,
          contextInput: draft.contextWindow === null ? '' : String(draft.contextWindow),
          rawMode: false,
          editing: false,
        },
        notice: undefined,
      };
      this.#emit();
      return true;
    } catch (error) {
      this.#notice('error', (error as Error).message);
      return false;
    }
  }

  async copyDefaults(): Promise<void> {
    const preset = this.#state.preset;
    if (!preset) return;
    try {
      const existing = await this.target.readText(this.config.presetPath);
      if (!existing) {
        this.#notice('info', 'There is no existing Preset file to copy settings from.');
        return;
      }
      const draft = copyDefaultsFromPreset(preset.draft, existing);
      const next = {
        ...preset,
        draft,
        raw: renderPreset(draft),
        defaultsSource: firstSectionName(existing),
      };
      this.#state = {
        ...this.#state,
        preset: next,
        notice: { kind: 'info', message: 'Copied runtime settings from the existing Preset.' },
      };
      this.#emit();
    } catch (error) {
      this.#notice('error', (error as Error).message);
    }
  }

  async writePresetNow(): Promise<void> {
    const preset = this.#state.preset;
    if (!preset) return;
    let draft = preset.draft;
    let rawText: string | undefined;
    if (preset.rawMode) {
      try {
        draft = applyRawPreset(preset.draft, preset.raw);
        rawText = preset.raw;
      } catch (error) {
        this.#notice('error', (error as Error).message);
        return;
      }
    } else {
      const value = Number(preset.contextInput.trim());
      if (!Number.isInteger(value) || value <= 0) {
        this.#notice('error', 'The Context Window must be a positive whole number.');
        return;
      }
      draft = { ...draft, contextWindow: value };
    }

    try {
      renderPreset(draft);
      const result = await writePreset({
        target: this.target,
        presetPath: this.config.presetPath,
        draft,
        ...(rawText !== undefined ? { rawText } : {}),
        overwriteAlias: this.#aliasConfirmed,
        now: this.#now,
      });
      if (this.#closed) return;
      const visible = await this.router.waitForPreset(draft.alias, {
        timeoutMs: 1_500,
        intervalMs: 100,
      });
      if (this.#closed) return;
      const nextPreset: PresetUiState = {
        ...this.#state.preset!,
        draft,
        raw: rawText ?? renderPreset(draft),
        visible,
        backupPath: result.backupPath,
        aliasConflict: undefined,
      };
      this.#state = {
        ...this.#state,
        screen: 'restart',
        preset: nextPreset,
        notice: visible
          ? undefined
          : {
              kind: 'info',
              message:
                'Preset written, but the Router does not list it yet. The restart may be required.',
            },
      };
      this.#aliasConfirmed = false;
      this.#emit();
    } catch (error) {
      this.#aliasConfirmed = false;
      if (error instanceof PresetError) {
        this.#notice('error', error.message);
        return;
      }
      if ((error as Error).name === 'AliasExistsError') {
        this.#state = {
          ...this.#state,
          screen: 'alias-confirm',
          preset: { ...this.#state.preset!, aliasConflict: draft.alias },
        };
        this.#emit();
        return;
      }
      this.#fail((error as Error).message, 'preset');
    }
  }

  async confirmAliasOverwrite(): Promise<void> {
    if (this.#state.screen !== 'alias-confirm') return;
    this.#aliasConfirmed = true;
    this.#state = {
      ...this.#state,
      screen: 'preset',
      preset: this.#state.preset
        ? { ...this.#state.preset, aliasConflict: undefined }
        : undefined,
    };
    await this.writePresetNow();
  }

  cancelAliasOverwrite(): void {
    if (this.#state.screen !== 'alias-confirm') return;
    this.#state = {
      ...this.#state,
      screen: 'preset',
      preset: this.#state.preset
        ? { ...this.#state.preset, aliasConflict: undefined }
        : undefined,
    };
    this.#emit();
  }

  // --- Restart --------------------------------------------------------------

  async confirmRestart(): Promise<void> {
    if (this.#state.screen !== 'restart') return;
    this.#state = { ...this.#state, screen: 'restarting', notice: undefined };
    this.#emit();
    try {
      const result = await this.target.run(this.config.restartCommand);
      if (this.#closed) return;
      if (result.code !== 0) {
        this.#fail(
          `Restart command exited with ${result.code}: ${result.stderr.trim() || result.stdout.trim()}`,
          'restart',
        );
        return;
      }
      const preset = this.#state.preset;
      const done: SessionState['done'] = {
        alias: preset?.draft.alias ?? '',
        modelPath: preset?.draft.modelPath ?? '',
      };
      if (preset?.backupPath) done.backupPath = preset.backupPath;
      this.#state = { ...this.#state, screen: 'done', done };
      this.#emit();
    } catch (error) {
      if (this.#closed) return;
      this.#fail((error as Error).message, 'restart');
    }
  }

  backToPreset(): void {
    if (this.#state.screen !== 'restart') return;
    this.#state = { ...this.#state, screen: 'preset' };
    this.#emit();
  }

  // --- Errors and lifecycle -------------------------------------------------

  async retry(): Promise<void> {
    const retry = this.#state.error?.retry;
    this.#state = { ...this.#state, error: undefined };
    if (retry === 'repo') {
      await this.openRepo(this.#state.urlInput);
    } else if (retry === 'download') {
      this.#state = { ...this.#state, screen: 'picker' };
      this.#emit();
      await this.confirmQuant();
    } else if (retry === 'preset') {
      this.#state = { ...this.#state, screen: 'preset' };
      this.#emit();
      await this.writePresetNow();
    } else if (retry === 'restart') {
      this.#state = { ...this.#state, screen: 'restart' };
      this.#emit();
      await this.confirmRestart();
    }
  }

  dismissError(): void {
    if (this.#state.error?.retry === 'repo') {
      this.#state = { ...this.#state, screen: 'url', error: undefined };
    } else if (this.#state.preset) {
      this.#state = { ...this.#state, screen: 'preset', error: undefined };
    } else {
      this.#state = { ...this.#state, screen: 'picker', error: undefined };
    }
    this.#emit();
  }

  quit(): void {
    this.#closed = true;
    this.#downloadAbort?.abort();
    this.#emit();
  }

  #fail(message: string, retry: RetryKind): void {
    this.#state = {
      ...this.#state,
      screen: 'error',
      error: { message, retry },
      progress: undefined,
      notice: undefined,
    };
    this.#emit();
  }

  #notice(kind: 'info' | 'error', message: string): void {
    this.#state = { ...this.#state, notice: { kind, message } };
    this.#emit();
  }

  #emit(): void {
    for (const listener of this.#listeners) listener(this.#state);
  }
}

function renderPresetSafe(draft: PresetDraft, fallback: string): string {
  try {
    return renderPreset(draft);
  } catch {
    return fallback;
  }
}

function firstSectionName(text: string): string | undefined {
  const match = /^\s*\[([^\]]+)\]/m.exec(text);
  return match?.[1]?.trim();
}
