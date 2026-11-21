# 04: Create or update a Preset for the downloaded Quant

**What to build:** After a download, the TUI offers a Preset draft: structured fields with an editable alias prefilled from repo and quant, a model path pointing at the downloaded file, and an editable Context Window field. A raw INI view gives full control, and writing the Preset is backed up and confirmed.

**Blocked by:** 03: Run the TUI directly on the Router host

**Status:** ready-for-agent

- [ ] A Preset draft is generated with an editable alias prefilled from repo and quant and a model path pointing at the downloaded file.
- [ ] Structured fields and a raw INI text view are both available and stay in sync.
- [ ] Settings from an existing Preset can be copied as defaults for the new one.
- [ ] A timestamped backup is written before the Preset file changes, and overwriting an existing alias requires confirmation.
- [ ] The new Preset is visible on the Router after writing, and no model is loaded automatically.
