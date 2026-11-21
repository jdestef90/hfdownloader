import assert from 'node:assert/strict';
import { readdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import test from 'node:test';

import {
  AliasExistsError,
  applyRawPreset,
  copyDefaultsFromPreset,
  createPresetDraft,
  parseIni,
  PresetError,
  renderPreset,
  upsertPreset,
  writePreset,
} from '../src/preset.ts';
import { LocalTarget } from '../src/transfer.ts';
import { withTempDir } from './helpers.ts';

const existingPreset = `[Gemma-4-12b]
allow-unauthenticated-tools = 1
cache-reuse = 256
jinja = 1
log-colors = off
model-draft = /models/mtp-gemma.gguf
spec-type = draft-mtp
warmup = false
batch-size = 2048
ctx-size = 200000
cache-type-k = q8_0
cache-type-v = q8_0
flash-attn = true
model = /models/gemma-4-12b.gguf
mmproj = /models/mmproj-gemma.gguf
n-gpu-layers = 99
parallel = 1
threads = 4

[Other]
model = /models/other.gguf
ctx-size = 4096
`;

test('parses INI Preset sections and their settings', () => {
  const document = parseIni(existingPreset);
  assert.deepEqual(document.sections.map((section) => section.name), [
    'Gemma-4-12b',
    'Other',
  ]);
  assert.equal(document.sections[0]!.entries.length, 17);
  assert.equal(
    document.sections[0]!.entries.find((entry) => entry.key === 'ctx-size')?.value,
    '200000',
  );
});

test('creates a Preset draft from the repo, Quant and downloaded files', () => {
  const draft = createPresetDraft({
    repo: 'unsloth/Qwen3-0.6B-GGUF',
    quantLabel: 'Q4_K_M',
    modelPath: '/Users/justin/models/gguf/Qwen3-0.6B-Q4_K_M.gguf',
    contextWindow: 40960,
    mmprojPath: '/Users/justin/models/gguf/mmproj-Qwen3-0.6B-f16.gguf',
    mtpPath: '/Users/justin/models/gguf/mtp-Qwen3-0.6B.gguf',
    existingPresetText: existingPreset,
  });

  assert.equal(draft.alias, 'Qwen3-0.6B-Q4_K_M');
  assert.equal(draft.modelPath, '/Users/justin/models/gguf/Qwen3-0.6B-Q4_K_M.gguf');
  assert.equal(draft.contextWindow, 40960);
  assert.equal(draft.mmprojPath, '/Users/justin/models/gguf/mmproj-Qwen3-0.6B-f16.gguf');
  assert.equal(draft.mtpPath, '/Users/justin/models/gguf/mtp-Qwen3-0.6B.gguf');

  const rendered = renderPreset(draft);
  assert.match(rendered, /^\[Qwen3-0\.6B-Q4_K_M\]$/m);
  assert.match(rendered, /^model = \/Users\/justin\/models\/gguf\/Qwen3-0\.6B-Q4_K_M\.gguf$/m);
  assert.match(rendered, /^ctx-size = 40960$/m);
  assert.match(rendered, /^mmproj = /m);
  assert.match(rendered, /^model-draft = /m);
  assert.match(rendered, /^spec-type = draft-mtp$/m);
  // Runtime settings are copied from the existing Preset, but not its identity or paths.
  assert.match(rendered, /^n-gpu-layers = 99$/m);
  assert.match(rendered, /^cache-type-k = q8_0$/m);
  assert.doesNotMatch(rendered, /Gemma-4-12b/);
  assert.doesNotMatch(rendered, /\/models\/gemma-4-12b\.gguf/);
  assert.doesNotMatch(rendered, /ctx-size = 200000/);
});

test('keeps structured fields and the raw INI view in sync', () => {
  const draft = createPresetDraft({
    repo: 'org/Model-GGUF',
    quantLabel: 'Q8_0',
    modelPath: '/models/Model-Q8_0.gguf',
    contextWindow: 8192,
  });

  const edited = applyRawPreset(
    draft,
    `[Custom-Alias]
model = /models/Model-Q8_0.gguf
ctx-size = 32768
threads = 8
`,
  );
  assert.equal(edited.alias, 'Custom-Alias');
  assert.equal(edited.contextWindow, 32768);
  assert.equal(
    edited.defaults.find((entry) => entry.key === 'threads')?.value,
    '8',
  );

  const rerendered = renderPreset({ ...edited, alias: 'Renamed' });
  assert.match(rerendered, /^\[Renamed\]$/m);
  assert.match(rerendered, /^ctx-size = 32768$/m);
  assert.match(rerendered, /^threads = 8$/m);
});

test('rejects malformed raw Preset text', () => {
  const draft = createPresetDraft({
    repo: 'org/Model-GGUF',
    quantLabel: 'Q8_0',
    modelPath: '/models/Model-Q8_0.gguf',
    contextWindow: 8192,
  });
  assert.throws(() => applyRawPreset(draft, 'not an ini'), PresetError);
  assert.throws(
    () => applyRawPreset(draft, '[A]\nctx-size = nope\n'),
    PresetError,
  );
  assert.throws(() => renderPreset({ ...draft, contextWindow: -1 }), PresetError);
  assert.throws(() => renderPreset({ ...draft, alias: '' }), PresetError);
});

test('copies defaults from an existing Preset', () => {
  const draft = createPresetDraft({
    repo: 'org/Model-GGUF',
    quantLabel: 'Q4_K_M',
    modelPath: '/models/Model.gguf',
    contextWindow: 4096,
  });
  const copied = copyDefaultsFromPreset(draft, existingPreset, 'Gemma-4-12b');
  assert.equal(copied.alias, draft.alias);
  assert.equal(copied.modelPath, draft.modelPath);
  assert.equal(
    copied.defaults.find((entry) => entry.key === 'n-gpu-layers')?.value,
    '99',
  );
  assert.equal(
    copied.defaults.find((entry) => entry.key === 'model'),
    undefined,
  );
  assert.equal(
    copied.defaults.find((entry) => entry.key === 'mmproj'),
    undefined,
  );
  assert.equal(
    copied.defaults.find((entry) => entry.key === 'spec-type'),
    undefined,
  );
});

test('updates only the target Preset section and preserves the rest', () => {
  const updated = upsertPreset(existingPreset, 'Other', '[Other]\nmodel = /models/new.gguf\nctx-size = 16384\n');
  const document = parseIni(updated);
  assert.deepEqual(document.sections.map((section) => section.name), [
    'Gemma-4-12b',
    'Other',
  ]);
  assert.match(updated, /^\[Gemma-4-12b\]$/m);
  assert.match(updated, /^model = \/models\/gemma-4-12b\.gguf$/m);
  assert.match(updated, /^ctx-size = 16384$/m);
  assert.doesNotMatch(updated, /ctx-size = 4096/);

  const appended = upsertPreset(existingPreset, 'New', '[New]\nmodel = /models/new.gguf\n');
  assert.deepEqual(parseIni(appended).sections.map((section) => section.name), [
    'Gemma-4-12b',
    'Other',
    'New',
  ]);
});

test('writes a timestamped backup before changing the Preset and confirms alias overwrite', async () => {
  await withTempDir(async (dir) => {
    const presetPath = join(dir, 'presets.ini');
    await writeFile(presetPath, existingPreset);
    const target = new LocalTarget();
    const draft = createPresetDraft({
      repo: 'org/Other-GGUF',
      quantLabel: 'Q4_K_M',
      modelPath: '/models/Other-Q4_K_M.gguf',
      contextWindow: 16384,
      existingPresetText: existingPreset,
    });

    await assert.rejects(
      () =>
        writePreset({
          target,
          presetPath,
          draft: { ...draft, alias: 'Other' },
          overwriteAlias: false,
          now: () => new Date('2026-11-21T02:30:00.000Z'),
        }),
      (error: unknown) => {
        assert.ok(error instanceof AliasExistsError);
        assert.equal(error.alias, 'Other');
        return true;
      },
    );

    const result = await writePreset({
      target,
      presetPath,
      draft: { ...draft, alias: 'Other' },
      overwriteAlias: true,
      now: () => new Date('2026-11-21T02:30:00.000Z'),
    });

    assert.equal(result.backupPath, `${presetPath}.bak-2026-11-21T02-30-00-000Z`);
    assert.equal(await readFile(result.backupPath!, 'utf8'), existingPreset);
    const written = await readFile(presetPath, 'utf8');
    assert.match(written, /^\[Other\]$/m);
    assert.match(written, /^model = \/models\/Other-Q4_K_M\.gguf$/m);
    assert.match(written, /^ctx-size = 16384$/m);
    assert.match(written, /^\[Gemma-4-12b\]$/m);

    const backups = (await readdir(dir)).filter((name) => name.includes('.bak-'));
    assert.equal(backups.length, 1);
  });
});

test('does not create a backup when the Preset file does not exist yet', async () => {
  await withTempDir(async (dir) => {
    const presetPath = join(dir, 'presets.ini');
    const draft = createPresetDraft({
      repo: 'org/New-GGUF',
      quantLabel: 'Q4_K_M',
      modelPath: '/models/New.gguf',
      contextWindow: 8192,
    });
    const result = await writePreset({
      target: new LocalTarget(),
      presetPath,
      draft,
      overwriteAlias: false,
      now: () => new Date('2026-11-21T02:30:00.000Z'),
    });
    assert.equal(result.backupPath, undefined);
    assert.match(await readFile(presetPath, 'utf8'), /^\[New-Q4_K_M\]$/m);
    assert.deepEqual(await readdir(dir), ['presets.ini']);
  });
});
