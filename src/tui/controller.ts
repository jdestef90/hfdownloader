import type { Session } from '../session.ts';

export interface Key {
  name?: string;
  ctrl?: boolean;
  meta?: boolean;
  shift?: boolean;
  sequence?: string;
}

function isEnter(key: Key): boolean {
  return key.name === 'return' || key.sequence === '\r' || key.sequence === '\n';
}

function isEscape(key: Key): boolean {
  return key.name === 'escape' || (key.sequence === '\x1b' && !key.name);
}

function isBackspace(key: Key): boolean {
  return key.name === 'backspace' || key.sequence === '\x7f' || key.sequence === '\b';
}

function isCtrl(key: Key, letter: string): boolean {
  return Boolean(key.ctrl) && key.name === letter;
}

function printable(key: Key): string | undefined {
  if (key.ctrl || key.meta) return undefined;
  if (key.name === 'space') return ' ';
  if (!key.sequence) return undefined;
  if (key.sequence.startsWith('\x1b')) return undefined;
  if ([...key.sequence].some((char) => char < ' ' || char === '\x7f')) return undefined;
  return key.sequence;
}

export class TuiController {
  readonly session: Session;
  readonly #onQuit: (() => void) | undefined;

  constructor(session: Session, onQuit?: () => void) {
    this.session = session;
    this.#onQuit = onQuit;
  }

  handleKey(key: Key): void {
    const state = this.session.state;
    if (isCtrl(key, 'c')) {
      this.#quit();
      return;
    }

    switch (state.screen) {
      case 'url':
        this.#handleUrl(key);
        return;
      case 'loading-repo':
        return;
      case 'picker':
        this.#handlePicker(key);
        return;
      case 'overwrite':
        if (isEnter(key) || printable(key)?.toLowerCase() === 'y') {
          this.#run(this.session.confirmOverwrite());
        } else if (isEscape(key) || printable(key)?.toLowerCase() === 'n') {
          this.session.cancelOverwrite();
        }
        return;
      case 'downloading':
        if (isEscape(key) || printable(key)?.toLowerCase() === 'c') {
          this.session.cancelDownload();
        }
        return;
      case 'preset':
        this.#handlePreset(key);
        return;
      case 'alias-confirm':
        if (isEnter(key) || printable(key)?.toLowerCase() === 'y') {
          this.#run(this.session.confirmAliasOverwrite());
        } else if (isEscape(key) || printable(key)?.toLowerCase() === 'n') {
          this.session.cancelAliasOverwrite();
        }
        return;
      case 'restart':
        if (isEnter(key)) {
          this.#run(this.session.confirmRestart());
        } else if (isEscape(key)) {
          this.session.backToPreset();
        } else if (printable(key)?.toLowerCase() === 'q') {
          this.#quit();
        }
        return;
      case 'restarting':
        return;
      case 'done':
        if (isEnter(key) || isEscape(key) || printable(key)?.toLowerCase() === 'q') {
          this.#quit();
        }
        return;
      case 'error':
        if (isEnter(key) || printable(key)?.toLowerCase() === 'r') {
          this.#run(this.session.retry());
        } else if (isEscape(key)) {
          this.session.dismissError();
        }
        return;
    }
  }

  #handleUrl(key: Key): void {
    if (isEnter(key)) {
      this.#run(this.session.submitUrl());
      return;
    }
    if (isBackspace(key)) {
      const value = this.session.state.urlInput;
      this.session.setUrlInput(value.slice(0, -1));
      return;
    }
    if (isEscape(key)) {
      this.session.setUrlInput('');
      return;
    }
    const text = printable(key);
    if (text) {
      this.session.setUrlInput(this.session.state.urlInput + text);
    }
  }

  #handlePicker(key: Key): void {
    if (key.name === 'up') {
      this.session.moveSelection(-1);
      return;
    }
    if (key.name === 'down') {
      this.session.moveSelection(1);
      return;
    }
    if (key.name === 'tab') {
      this.session.togglePickerFocus();
      return;
    }
    if (printable(key) === ' ') {
      if (this.session.state.pickerFocus === 'sidecars') {
        this.session.toggleSidecar();
      }
      return;
    }
    if (isEnter(key)) {
      this.#run(this.session.confirmQuant());
      return;
    }
    if (isEscape(key) || printable(key)?.toLowerCase() === 'q') {
      this.#quit();
    }
  }

  #handlePreset(key: Key): void {
    const preset = this.session.state.preset;
    if (!preset) return;

    if (preset.rawMode) {
      if (isCtrl(key, 's')) {
        this.#run(this.session.writePresetNow());
        return;
      }
      if (isEscape(key)) {
        this.session.toggleRawMode();
        return;
      }
      if (isEnter(key)) {
        this.session.setRawText(`${preset.raw}\n`);
        return;
      }
      if (isBackspace(key)) {
        this.session.setRawText(preset.raw.slice(0, -1));
        return;
      }
      const text = printable(key);
      if (text) {
        this.session.setRawText(preset.raw + text);
      }
      return;
    }

    if (isCtrl(key, 's')) {
      this.#run(this.session.writePresetNow());
      return;
    }
    if (key.name === 'up') {
      this.session.moveSelection(-1);
      return;
    }
    if (key.name === 'down' || key.name === 'tab') {
      this.session.moveSelection(1);
      return;
    }

    if (preset.editing) {
      if (isEnter(key)) {
        this.session.endPresetEdit();
        return;
      }
      if (isEscape(key)) {
        this.session.endPresetEdit();
        return;
      }
      if (key.name === 'up' || key.name === 'down') {
        this.session.endPresetEdit();
        if (this.session.state.preset?.editing) return;
        this.session.moveSelection(key.name === 'up' ? -1 : 1);
        return;
      }
      if (isBackspace(key)) {
        if (preset.field === 0) {
          this.session.setAlias(preset.draft.alias.slice(0, -1));
        } else if (preset.field === 1) {
          this.session.setContextInput(preset.contextInput.slice(0, -1));
        }
        return;
      }
      const text = printable(key);
      if (text) {
        if (preset.field === 0) {
          this.session.setAlias(preset.draft.alias + text);
        } else if (preset.field === 1) {
          this.session.setContextInput(preset.contextInput + text);
        }
      }
      return;
    }

    if (isEnter(key)) {
      if (preset.field === 2) {
        this.#run(this.session.writePresetNow());
      } else {
        this.session.beginPresetEdit();
      }
      return;
    }
    const action = printable(key)?.toLowerCase();
    if (action === 'r') {
      this.session.toggleRawMode();
    } else if (action === 'd') {
      this.#run(this.session.copyDefaults());
    } else if (action === 'q' || isEscape(key)) {
      this.#quit();
    }
  }

  #run(promise: Promise<void>): void {
    void this.session.track(promise).catch(() => undefined);
  }

  #quit(): void {
    this.session.quit();
    this.#onQuit?.();
  }
}
