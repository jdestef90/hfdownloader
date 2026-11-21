import type { SessionState } from '../session.ts';

export interface RenderOptions {
  width?: number;
  color?: boolean;
}

const RULE = '─';

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return '—';
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let value = bytes / 1024;
  let index = 0;
  while (value >= 1024 && index < units.length - 1) {
    value /= 1024;
    index += 1;
  }
  return `${value >= 100 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
}

export function formatEta(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '—';
  if (seconds < 1) return '<1s';
  const rounded = Math.ceil(seconds);
  const minutes = Math.floor(rounded / 60);
  const rest = rounded % 60;
  if (minutes === 0) return `${rest}s`;
  const hours = Math.floor(minutes / 60);
  const mins = minutes % 60;
  if (hours === 0) return `${mins}m ${rest}s`;
  return `${hours}h ${mins}m`;
}

export function progressBar(transferred: number, total: number, width = 28): string {
  if (total <= 0) return '[' + '░'.repeat(width) + ']';
  const ratio = Math.min(1, Math.max(0, transferred / total));
  const filled = Math.round(ratio * width);
  return `[${'█'.repeat(filled)}${'░'.repeat(width - filled)}]`;
}

function clip(value: string, width: number): string {
  if (value.length <= width) return value;
  return width <= 1 ? value.slice(0, width) : `${value.slice(0, width - 1)}…`;
}

function heading(title: string, width: number): string[] {
  return [title, RULE.repeat(Math.max(1, Math.min(width, 72)))];
}

function contextSourceLabel(preset: NonNullable<SessionState['preset']>): string {
  if (preset.suggestionPending) return 'asking helper model…';
  if (preset.contextSource === 'helper') return 'suggested by helper model';
  if (preset.contextSource === 'hub') return 'published Hub limit';
  return 'not set';
}

function renderPreset(state: SessionState, width: number): string[] {
  const preset = state.preset;
  if (!preset) return ['No Preset draft is available.'];
  const lines: string[] = [];
  lines.push(`Preset draft for ${preset.draft.alias || '(unnamed)'}`);
  lines.push('');

  if (preset.rawMode) {
    lines.push('Raw INI (Ctrl-S write, Esc back to fields)');
    for (const line of preset.raw.split(/\r?\n/)) {
      lines.push(clip(`  ${line}`, width));
    }
    return lines;
  }

  const marker = (field: number): string => (preset.field === field ? '>' : ' ');
  const editMark = (field: number): string =>
    preset.field === field && preset.editing ? '█' : '';
  lines.push(
    `${marker(0)} Alias: ${preset.draft.alias}${editMark(0)}`,
  );
  lines.push(
    `${marker(1)} Context Window: ${preset.contextInput}${editMark(1)}  (${contextSourceLabel(preset)})`,
  );
  lines.push(`  Model: ${preset.draft.modelPath}`);
  if (preset.draft.mmprojPath) lines.push(`  mmproj: ${preset.draft.mmprojPath}`);
  if (preset.draft.mtpPath) lines.push(`  mtp: ${preset.draft.mtpPath}`);
  lines.push('');
  if (preset.defaultsSource) {
    lines.push(`Defaults copied from [${preset.defaultsSource}]`);
  }
  lines.push(`${marker(2)} [ Write Preset ]`);
  lines.push('');
  lines.push('↑/↓ field · Enter edit · r raw INI · d copy defaults · Ctrl-S write');
  return lines;
}

function renderPicker(state: SessionState, width: number): string[] {
  const repo = state.repo;
  if (!repo) return ['No repository loaded.'];
  const lines: string[] = [];
  const context = repo.contextLength !== undefined ? ` · context ${repo.contextLength}` : '';
  lines.push(`Repository: ${repo.repo}${context}`);
  lines.push('');
  lines.push('Quants');
  repo.quants.forEach((quant, index) => {
    const selected = index === state.selectedQuant ? '>' : ' ';
    const shards = quant.files.length > 1 ? ` · ${quant.files.length} shards` : '';
    lines.push(
      clip(
        `${selected} ${quant.label.padEnd(18)} ${formatBytes(quant.size).padStart(9)}${shards}`,
        width,
      ),
    );
  });
  lines.push('');
  if (repo.sidecars.length === 0) {
    lines.push('Sidecars: none detected');
  } else {
    lines.push('Sidecars (optional)');
    repo.sidecars.forEach((sidecar, index) => {
      const checked = state.selectedSidecars.includes(sidecar.path) ? 'x' : ' ';
      const cursor =
        state.pickerFocus === 'sidecars' && index === state.sidecarCursor ? '>' : ' ';
      lines.push(
        clip(
          `${cursor} [${checked}] ${sidecar.kind.padEnd(5)} ${sidecar.path} (${formatBytes(sidecar.size)})`,
          width,
        ),
      );
    });
  }
  lines.push('');
  lines.push('↑/↓ move · Tab focus · Space toggle Sidecar · Enter download · Esc quit');
  return lines;
}

export function renderApp(state: SessionState, options: RenderOptions = {}): string {
  const width = options.width ?? 80;
  const lines: string[] = heading('HF Downloader', width);

  switch (state.screen) {
    case 'url':
      lines.push('Paste a Hugging Face GGUF repository URL:');
      lines.push('');
      lines.push(`> ${state.urlInput}█`);
      lines.push('');
      lines.push('Enter browse · Ctrl-C quit');
      break;
    case 'loading-repo':
      lines.push(`Loading Quants from ${state.urlInput}…`);
      break;
    case 'picker':
      lines.push(...renderPicker(state, width));
      break;
    case 'overwrite':
      lines.push('These files already exist in the GGUF Directory:');
      for (const path of state.overwritePaths) lines.push(`  ${path}`);
      lines.push('');
      lines.push('Overwrite them? y / n');
      break;
    case 'downloading': {
      const progress = state.progress;
      lines.push(`Downloading ${state.repo?.quants[state.selectedQuant]?.label ?? ''}`);
      if (progress) {
        const ratio = progress.total > 0 ? progress.transferred / progress.total : 0;
        lines.push(
          `${progressBar(progress.transferred, progress.total)} ${(ratio * 100).toFixed(0)}%  ` +
            `${formatBytes(progress.transferred)} / ${formatBytes(progress.total)}`,
        );
        lines.push(
          `${formatBytes(progress.speed)}/s · ETA ${formatEta(progress.etaSeconds)} · ` +
            `${progress.file} (${Math.min(progress.completedFiles + 1, progress.totalFiles)}/${progress.totalFiles})`,
        );
      }
      lines.push('');
      lines.push('Esc cancel download');
      break;
    }
    case 'preset':
      lines.push(...renderPreset(state, width));
      break;
    case 'alias-confirm':
      lines.push(`A Preset named [${state.preset?.aliasConflict ?? state.preset?.draft.alias}] already exists.`);
      lines.push('');
      lines.push('Overwrite it? A timestamped backup will be written first. y / n');
      break;
    case 'restart':
      lines.push(`Preset [${state.preset?.draft.alias ?? ''}] written.`);
      lines.push('');
      lines.push('Restart command:');
      lines.push(`  ${state.restartCommand}`);
      lines.push('');
      lines.push('Enter run restart · Esc edit Preset · No model is loaded automatically');
      break;
    case 'restarting':
      lines.push('Running the restart command…');
      break;
    case 'done':
      lines.push('Done.');
      lines.push('');
      lines.push(`Preset: [${state.done?.alias ?? ''}]`);
      lines.push(`Model: ${state.done?.modelPath ?? ''}`);
      if (state.done?.backupPath) lines.push(`Backup: ${state.done.backupPath}`);
      lines.push('');
      lines.push('The Router was restarted. No model was loaded automatically.');
      lines.push('Enter or q to quit.');
      break;
    case 'error':
      lines.push('Something went wrong:');
      lines.push('');
      lines.push(state.error?.message ?? 'Unknown error');
      lines.push('');
      lines.push('Enter retry · Esc dismiss');
      break;
  }

  if (state.notice) {
    lines.push('');
    lines.push(
      clip(
        `${state.notice.kind === 'error' ? 'Error: ' : 'Note: '}${state.notice.message}`,
        width,
      ),
    );
  }

  return lines.map((line) => clip(line, width)).join('\n') + '\n';
}
