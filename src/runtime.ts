import type { Config } from './config.ts';
import { ConfigError } from './config.ts';
import { LocalTarget, SshTarget, type Target } from './transfer.ts';

export function createTarget(config: Config): Target {
  if (config.mode === 'local') {
    return new LocalTarget();
  }
  if (!config.sshTarget) {
    throw new ConfigError('SSH mode requires sshTarget (HFDOWNLOADER_SSH_TARGET).');
  }
  return new SshTarget({ target: config.sshTarget });
}
