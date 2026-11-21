import type { Target } from './transfer.ts';

export interface IniEntry {
  key: string;
  value: string;
}

export interface IniSection {
  name: string;
  entries: IniEntry[];
}

export interface IniDocument {
  sections: IniSection[];
}

export interface PresetDraft {
  alias: string;
  modelPath: string;
  contextWindow: number | null;
  mmprojPath?: string;
  mtpPath?: string;
  defaults: IniEntry[];
}

export class PresetError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'PresetError';
  }
}

export class AliasExistsError extends Error {
  readonly alias: string;
  constructor(alias: string) {
    super(`A Preset named "${alias}" already exists. Confirm overwriting it.`);
    this.name = 'AliasExistsError';
    this.alias = alias;
  }
}

const RESERVED_KEYS = new Set(['model', 'ctx-size', 'mmproj', 'model-draft', 'spec-type']);

const SECTION_PATTERN = /^\s*\[([^\]]+)\]\s*$/;
const ENTRY_PATTERN = /^\s*([^=;#][^=]*?)\s*=\s*(.*?)\s*$/;

export function parseIni(text: string): IniDocument {
  const sections: IniSection[] = [];
  let current: IniSection | undefined;
  for (const line of text.split(/\r?\n/)) {
    const section = SECTION_PATTERN.exec(line);
    if (section?.[1]) {
      current = { name: section[1].trim(), entries: [] };
      sections.push(current);
      continue;
    }
    if (!current || line.trim() === '' || line.trimStart().startsWith('#')) {
      continue;
    }
    if (line.trimStart().startsWith(';')) {
      continue;
    }
    const entry = ENTRY_PATTERN.exec(line);
    if (entry?.[1] !== undefined) {
      current.entries.push({ key: entry[1].trim(), value: entry[2] ?? '' });
    }
  }
  return { sections };
}

export function findSection(text: string, alias?: string): IniSection | undefined {
  const document = parseIni(text);
  if (alias === undefined) return document.sections[0];
  return document.sections.find((section) => section.name === alias);
}

function sanitizeAlias(value: string): string {
  return value
    .replace(/[^A-Za-z0-9._-]+/g, '-')
    .replace(/-+/g, '-')
    .replace(/^[-.]+|[-.]+$/g, '');
}

function aliasFrom(repo: string, quantLabel: string): string {
  const repoName = (repo.split('/').pop() ?? repo).replace(/-GGUF$/i, '');
  const base = sanitizeAlias(repoName) || 'model';
  const quant = sanitizeAlias(quantLabel);
  return quant ? `${base}-${quant}` : base;
}

function validateAlias(alias: string): string {
  const trimmed = alias.trim();
  if (trimmed === '') {
    throw new PresetError('The Preset alias cannot be empty.');
  }
  if (/[\[\]\r\n]/.test(trimmed)) {
    throw new PresetError('The Preset alias cannot contain brackets or newlines.');
  }
  return trimmed;
}

function validateContextWindow(value: number | null): void {
  if (value === null) return;
  if (!Number.isInteger(value) || value <= 0) {
    throw new PresetError('The Context Window must be a positive whole number.');
  }
}

function validatePath(value: string, label: string): string {
  const trimmed = value.trim();
  if (trimmed === '') {
    throw new PresetError(`${label} cannot be empty.`);
  }
  if (/[\r\n]/.test(trimmed)) {
    throw new PresetError(`${label} cannot contain newlines.`);
  }
  return trimmed;
}

function sectionLines(name: string, entries: IniEntry[]): string[] {
  return [`[${name}]`, ...entries.map((entry) => `${entry.key} = ${entry.value}`)];
}

function defaultsFromSection(section: IniSection | undefined): IniEntry[] {
  if (!section) return [];
  return section.entries
    .filter((entry) => !RESERVED_KEYS.has(entry.key))
    .map((entry) => ({ ...entry }));
}

export interface CreatePresetDraftOptions {
  repo: string;
  quantLabel: string;
  modelPath: string;
  contextWindow?: number;
  mmprojPath?: string;
  mtpPath?: string;
  existingPresetText?: string;
  defaultsAlias?: string;
}

export function createPresetDraft(options: CreatePresetDraftOptions): PresetDraft {
  const defaults = options.existingPresetText
    ? defaultsFromSection(findSection(options.existingPresetText, options.defaultsAlias))
    : [];
  const draft: PresetDraft = {
    alias: aliasFrom(options.repo, options.quantLabel),
    modelPath: validatePath(options.modelPath, 'The model path'),
    contextWindow: options.contextWindow ?? null,
    defaults,
  };
  if (options.mmprojPath) draft.mmprojPath = validatePath(options.mmprojPath, 'The mmproj path');
  if (options.mtpPath) draft.mtpPath = validatePath(options.mtpPath, 'The mtp path');
  return draft;
}

export function copyDefaultsFromPreset(
  draft: PresetDraft,
  presetText: string,
  alias?: string,
): PresetDraft {
  const section = findSection(presetText, alias);
  if (!section) {
    throw new PresetError(
      alias ? `No Preset named "${alias}" was found.` : 'The Preset file has no sections.',
    );
  }
  return { ...draft, defaults: defaultsFromSection(section) };
}

export function renderPreset(draft: PresetDraft): string {
  const alias = validateAlias(draft.alias);
  const modelPath = validatePath(draft.modelPath, 'The model path');
  validateContextWindow(draft.contextWindow);

  const entries: IniEntry[] = draft.defaults
    .filter((entry) => !RESERVED_KEYS.has(entry.key))
    .map((entry) => ({ ...entry }));
  const set = (key: string, value: string): void => {
    const existing = entries.find((entry) => entry.key === key);
    if (existing) existing.value = value;
    else entries.push({ key, value });
  };

  set('model', modelPath);
  if (draft.contextWindow !== null) {
    set('ctx-size', String(draft.contextWindow));
  }
  if (draft.mmprojPath) {
    set('mmproj', validatePath(draft.mmprojPath, 'The mmproj path'));
  }
  if (draft.mtpPath) {
    set('model-draft', validatePath(draft.mtpPath, 'The mtp path'));
    set('spec-type', 'draft-mtp');
  }

  return `${sectionLines(alias, entries).join('\n')}\n`;
}

export function applyRawPreset(draft: PresetDraft, raw: string): PresetDraft {
  const document = parseIni(raw);
  if (document.sections.length !== 1) {
    throw new PresetError('The raw view must contain exactly one Preset section.');
  }
  const section = document.sections[0]!;
  const alias = validateAlias(section.name);
  const values = new Map(section.entries.map((entry) => [entry.key, entry.value]));
  const modelPath = validatePath(values.get('model') ?? '', 'The model path');

  let contextWindow: number | null = null;
  const rawContext = values.get('ctx-size');
  if (rawContext !== undefined) {
    contextWindow = Number(rawContext);
    validateContextWindow(contextWindow);
  }

  const result: PresetDraft = {
    alias,
    modelPath,
    contextWindow,
    defaults: defaultsFromSection(section),
  };
  const mmproj = values.get('mmproj');
  if (mmproj) result.mmprojPath = validatePath(mmproj, 'The mmproj path');
  const mtp = values.get('model-draft');
  if (mtp) result.mtpPath = validatePath(mtp, 'The mtp path');
  return result;
}

function sectionBlock(text: string, alias: string): string[] {
  const lines = text.split(/\r?\n/);
  let start = -1;
  let end = lines.length;
  for (let index = 0; index < lines.length; index += 1) {
    const match = SECTION_PATTERN.exec(lines[index] ?? '');
    if (!match?.[1]) continue;
    if (start === -1) {
      start = index;
    } else {
      end = index;
      break;
    }
  }
  if (start === -1) {
    throw new PresetError('The Preset section is missing.');
  }
  const block = lines.slice(start, end);
  while (block.length > 0 && block[block.length - 1]!.trim() === '') {
    block.pop();
  }
  block[0] = `[${alias}]`;
  return block;
}

export function upsertPreset(existing: string, alias: string, sectionText: string): string {
  const replacement = sectionBlock(sectionText, alias);
  const lines = existing === '' ? [] : existing.split(/\r?\n/);
  let start = -1;
  let end = lines.length;
  for (let index = 0; index < lines.length; index += 1) {
    const match = SECTION_PATTERN.exec(lines[index] ?? '');
    if (!match?.[1]) continue;
    if (start === -1 && match[1].trim() === alias) {
      start = index;
      continue;
    }
    if (start !== -1) {
      end = index;
      break;
    }
  }

  if (start === -1) {
    while (lines.length > 0 && lines[lines.length - 1]!.trim() === '') {
      lines.pop();
    }
    if (lines.length > 0) lines.push('');
    lines.push(...replacement);
  } else {
    lines.splice(start, end - start, ...replacement);
  }

  while (lines.length > 0 && lines[lines.length - 1]!.trim() === '') {
    lines.pop();
  }
  return `${lines.join('\n')}\n`;
}

export interface WritePresetOptions {
  target: Target;
  presetPath: string;
  draft: PresetDraft;
  rawText?: string;
  overwriteAlias: boolean;
  now?: () => Date;
}

export interface WritePresetResult {
  backupPath?: string;
  content: string;
}

function backupStamp(now: Date): string {
  return now.toISOString().replace(/[:.]/g, '-');
}

export async function writePreset(options: WritePresetOptions): Promise<WritePresetResult> {
  const { target, presetPath, draft, overwriteAlias } = options;
  const existing = await target.readText(presetPath);
  const document = existing === null ? { sections: [] } : parseIni(existing);
  const exists = document.sections.some((section) => section.name === draft.alias);
  if (exists && !overwriteAlias) {
    throw new AliasExistsError(draft.alias);
  }

  let backupPath: string | undefined;
  if (existing !== null) {
    backupPath = `${presetPath}.bak-${backupStamp((options.now ?? (() => new Date()))())}`;
    await target.writeText(backupPath, existing);
  }

  const sectionText = options.rawText ?? renderPreset(draft);
  const content = upsertPreset(existing ?? '', draft.alias, sectionText);
  await target.writeText(presetPath, content);

  const result: WritePresetResult = { content };
  if (backupPath) result.backupPath = backupPath;
  return result;
}
