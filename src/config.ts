import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

export type Mode = 'ssh' | 'local';

export interface Config {
  mode: Mode;
  routerUrl: string;
  sshTarget?: string;
  ggufDirectory: string;
  presetPath: string;
  restartCommand: string;
  helperModel: string;
  hubToken?: string;
  routerApiKey?: string;
  helperTimeoutMs: number;
  configPath?: string;
}

export class ConfigError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ConfigError';
  }
}

interface LoadConfigOptions {
  env?: NodeJS.ProcessEnv;
  configPath?: string;
}

const ENV_KEYS = {
  mode: 'HFDOWNLOADER_MODE',
  routerUrl: 'HFDOWNLOADER_ROUTER_URL',
  sshTarget: 'HFDOWNLOADER_SSH_TARGET',
  ggufDirectory: 'HFDOWNLOADER_GGUF_DIRECTORY',
  presetPath: 'HFDOWNLOADER_PRESET_PATH',
  restartCommand: 'HFDOWNLOADER_RESTART_COMMAND',
  helperModel: 'HFDOWNLOADER_HELPER_MODEL',
  hubToken: 'HFDOWNLOADER_HUB_TOKEN',
  routerApiKey: 'HFDOWNLOADER_ROUTER_API_KEY',
  helperTimeoutMs: 'HFDOWNLOADER_HELPER_TIMEOUT_MS',
} as const;

const FILE_KEYS = new Set([
  'mode',
  'routerUrl',
  'sshTarget',
  'ggufDirectory',
  'presetPath',
  'restartCommand',
  'helperModel',
  'hubToken',
  'routerApiKey',
  'helperTimeoutMs',
]);

export function defaultConfigPath(env: NodeJS.ProcessEnv = process.env): string {
  const base = env.XDG_CONFIG_HOME?.trim() || join(homedir(), '.config');
  return join(base, 'hfdownloader', 'config.json');
}

function readConfigFile(path: string, explicit: boolean): Record<string, unknown> {
  let text: string;
  try {
    text = readFileSync(path, 'utf8');
  } catch (error) {
    if (!explicit && (error as NodeJS.ErrnoException).code === 'ENOENT') {
      return {};
    }
    throw new ConfigError(`Cannot read config file ${path}: ${(error as Error).message}`);
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (error) {
    throw new ConfigError(`Config file ${path} is not valid JSON: ${(error as Error).message}`);
  }

  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new ConfigError(`Config file ${path} must contain a JSON object.`);
  }

  const values: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(parsed)) {
    if (FILE_KEYS.has(key)) {
      values[key] = value;
    }
  }
  return values;
}

function envValue(env: NodeJS.ProcessEnv, key: string): string | undefined {
  const value = env[key];
  if (value === undefined) {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === '' ? undefined : trimmed;
}

function asString(value: unknown): string | undefined {
  if (typeof value !== 'string') {
    return undefined;
  }
  const trimmed = value.trim();
  return trimmed === '' ? undefined : trimmed;
}

function asNumber(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === 'string' && value.trim() !== '') {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return undefined;
}

function expandHome(path: string): string {
  if (path === '~') {
    return homedir();
  }
  if (path.startsWith('~/')) {
    return join(homedir(), path.slice(2));
  }
  return path;
}

function normalizeUrl(value: string): string {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new ConfigError(`routerUrl is not a valid URL: ${value}`);
  }
  if (url.protocol !== 'http:' && url.protocol !== 'https:') {
    throw new ConfigError(`routerUrl must use http or https: ${value}`);
  }
  return value.replace(/\/+$/, '');
}

export function loadConfig(options: LoadConfigOptions = {}): Config {
  const env = options.env ?? process.env;
  const explicitPath = options.configPath ?? envValue(env, 'HFDOWNLOADER_CONFIG');
  const configPath = explicitPath ?? defaultConfigPath(env);
  const file = readConfigFile(configPath, Boolean(explicitPath));

  const pick = (key: keyof typeof ENV_KEYS): unknown => {
    const fromEnv = envValue(env, ENV_KEYS[key]);
    return fromEnv ?? file[key];
  };

  const rawMode = asString(pick('mode')) ?? 'ssh';
  if (rawMode !== 'ssh' && rawMode !== 'local') {
    throw new ConfigError(`Unknown mode "${rawMode}". Expected "ssh" or "local".`);
  }
  const mode: Mode = rawMode;

  const routerUrl = asString(pick('routerUrl')) ?? 'http://127.0.0.1:8080';
  const sshTarget = asString(pick('sshTarget'));
  const ggufDirectoryRaw = asString(pick('ggufDirectory'));
  const presetPathRaw = asString(pick('presetPath'));
  const restartCommand = asString(pick('restartCommand'));
  const helperModel = asString(pick('helperModel'));
  const hubToken = asString(pick('hubToken'));
  const routerApiKey = asString(pick('routerApiKey'));
  const helperTimeoutMs = asNumber(pick('helperTimeoutMs')) ?? 15_000;

  const missing: string[] = [];
  if (mode === 'ssh' && !sshTarget) missing.push('sshTarget');
  if (!ggufDirectoryRaw) missing.push('ggufDirectory');
  if (!presetPathRaw) missing.push('presetPath');
  if (!restartCommand) missing.push('restartCommand');
  if (!helperModel) missing.push('helperModel');

  if (missing.length > 0) {
    throw new ConfigError(
      `Missing required configuration: ${missing.join(', ')}. ` +
        `Set them in ${configPath} or with HFDOWNLOADER_* environment variables.`,
    );
  }

  const ggufDirectory =
    mode === 'local' ? expandHome(ggufDirectoryRaw!) : ggufDirectoryRaw!;
  const presetPath = mode === 'local' ? expandHome(presetPathRaw!) : presetPathRaw!;

  if (!Number.isInteger(helperTimeoutMs) || helperTimeoutMs <= 0) {
    throw new ConfigError('helperTimeoutMs must be a positive integer.');
  }

  const config: Config = {
    mode,
    routerUrl: normalizeUrl(routerUrl),
    ggufDirectory,
    presetPath,
    restartCommand: restartCommand!,
    helperModel: helperModel!,
    helperTimeoutMs,
    configPath,
  };
  if (sshTarget) config.sshTarget = sshTarget;
  if (hubToken) config.hubToken = hubToken;
  if (routerApiKey) config.routerApiKey = routerApiKey;
  return config;
}
