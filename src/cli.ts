#!/usr/bin/env node
import { ConfigError, loadConfig } from './config.ts';
import { HubClient } from './hub.ts';
import { RouterClient } from './router.ts';
import { createTarget } from './runtime.ts';
import { Session } from './session.ts';
import { runTui } from './tui/run.ts';

const VERSION = '0.1.0';

const HELP = `hfdownloader ${VERSION}

Browse GGUF Quants from a Hugging Face repository, download one into the
Router host's GGUF Directory, draft a Preset, and restart the Router.

Usage:
  hfdownloader [huggingface-url] [options]

Options:
  -c, --config <path>   Config file (default: $XDG_CONFIG_HOME/hfdownloader/config.json)
  -m, --mode <mode>     "ssh" (default) or "local"
  -h, --help            Show this help
  -v, --version         Show the version

Configuration keys:
  mode, routerUrl, sshTarget, ggufDirectory, presetPath, restartCommand,
  helperModel, hubToken, routerApiKey, helperTimeoutMs

Environment overrides:
  HFDOWNLOADER_CONFIG, HFDOWNLOADER_MODE, HFDOWNLOADER_ROUTER_URL,
  HFDOWNLOADER_SSH_TARGET, HFDOWNLOADER_GGUF_DIRECTORY,
  HFDOWNLOADER_PRESET_PATH, HFDOWNLOADER_RESTART_COMMAND,
  HFDOWNLOADER_HELPER_MODEL, HFDOWNLOADER_HUB_TOKEN,
  HFDOWNLOADER_ROUTER_API_KEY, HFDOWNLOADER_HELPER_TIMEOUT_MS
`;

interface CliOptions {
  url?: string;
  configPath?: string;
  mode?: 'ssh' | 'local';
  help?: boolean;
  version?: boolean;
}

function parseArgs(argv: string[]): CliOptions {
  const options: CliOptions = {};
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]!;
    if (arg === '--help' || arg === '-h') {
      options.help = true;
    } else if (arg === '--version' || arg === '-v') {
      options.version = true;
    } else if (arg === '--config' || arg === '-c') {
      const value = argv[index + 1];
      if (!value) throw new ConfigError(`${arg} needs a path.`);
      options.configPath = value;
      index += 1;
    } else if (arg === '--mode' || arg === '-m') {
      const value = argv[index + 1];
      if (value !== 'ssh' && value !== 'local') {
        throw new ConfigError('--mode must be "ssh" or "local".');
      }
      options.mode = value;
      index += 1;
    } else if (arg.startsWith('-')) {
      throw new ConfigError(`Unknown option: ${arg}`);
    } else if (!options.url) {
      options.url = arg;
    } else {
      throw new ConfigError(`Unexpected argument: ${arg}`);
    }
  }
  return options;
}

async function main(): Promise<void> {
  let options: CliOptions;
  try {
    options = parseArgs(process.argv.slice(2));
  } catch (error) {
    console.error((error as Error).message);
    console.error('Run hfdownloader --help for usage.');
    process.exitCode = 2;
    return;
  }

  if (options.help) {
    process.stdout.write(HELP);
    return;
  }
  if (options.version) {
    process.stdout.write(`${VERSION}\n`);
    return;
  }

  const env = { ...process.env };
  if (options.mode) env.HFDOWNLOADER_MODE = options.mode;

  let config;
  try {
    config = loadConfig({
      env,
      ...(options.configPath ? { configPath: options.configPath } : {}),
    });
  } catch (error) {
    if (error instanceof ConfigError) {
      console.error(`Configuration error: ${error.message}`);
      process.exitCode = 1;
      return;
    }
    throw error;
  }

  let target;
  try {
    target = createTarget(config);
  } catch (error) {
    console.error(`Configuration error: ${(error as Error).message}`);
    process.exitCode = 1;
    return;
  }

  const hub = new HubClient({
    ...(config.hubToken ? { token: config.hubToken } : {}),
  });
  const router = new RouterClient({
    baseUrl: config.routerUrl,
    ...(config.routerApiKey ? { apiKey: config.routerApiKey } : {}),
  });
  const session = new Session({
    config,
    hub,
    target,
    router,
    ...(options.url ? { initialUrl: options.url } : {}),
  });

  const onSignal = (): void => session.quit();
  process.once('SIGINT', onSignal);
  process.once('SIGTERM', onSignal);

  await session.start();
  await runTui({ session });
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack ?? error.message : String(error));
  process.exitCode = 1;
});
