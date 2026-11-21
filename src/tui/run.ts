import { emitKeypressEvents } from 'node:readline';
import type { Readable, Writable } from 'node:stream';

import type { Session } from '../session.ts';
import { TuiController, type Key } from './controller.ts';
import { renderApp } from './render.ts';

export interface RunTuiOptions {
  session: Session;
  stdin?: Readable & { isTTY?: boolean; setRawMode?: (mode: boolean) => void };
  stdout?: Writable & { columns?: number };
}

export async function runTui(options: RunTuiOptions): Promise<void> {
  const { session } = options;
  const stdin = options.stdin ?? process.stdin;
  const stdout = options.stdout ?? process.stdout;
  let finished = false;
  let finish: () => void = () => undefined;

  const draw = (): void => {
    if (finished) return;
    const width = stdout.columns ?? 80;
    stdout.write('\x1b[2J\x1b[H\x1b[?25l');
    stdout.write(renderApp(session.state, { width }));
    stdout.write('\x1b[?25h');
  };

  const stop = (): void => {
    if (!finished) {
      finished = true;
      finish();
    }
  };
  const controller = new TuiController(session, stop);
  const unsubscribe = session.subscribe(() => {
    if (session.closed) {
      stop();
      return;
    }
    draw();
  });
  const onKey = (_input: string, key: Key): void => {
    controller.handleKey(key);
    if (session.closed) stop();
  };

  if (stdin.isTTY) {
    stdin.setRawMode?.(true);
  }
  emitKeypressEvents(stdin);
  stdin.resume();
  stdin.on('keypress', onKey);
  draw();

  try {
    await new Promise<void>((resolve) => {
      finish = resolve;
      if (session.closed) resolve();
    });
  } finally {
    stdin.off('keypress', onKey);
    unsubscribe();
    if (stdin.isTTY) stdin.setRawMode?.(false);
    stdout.write('\x1b[?25h');
  }
}
