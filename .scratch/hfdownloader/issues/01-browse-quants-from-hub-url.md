# 01: Browse Quants from a Hub URL

**What to build:** Launch the TUI with a Hugging Face URL argument or paste one into a bare launch; configuration loads; the Hub repo's GGUF Quants appear in a picker with quant label, size, grouped shards, and detected mmproj/mtp Sidecars. Read-only — no files are written.

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent

**Handoff:** `/tmp/opencode/hfdownloader-handoff.md` — conversation summary and environment facts for this work.

- [ ] The TUI accepts a Hub URL as a command-line argument and via an interactive paste field, with identical picker behavior.
- [ ] Configuration loads from one file with environment-variable overrides, covering Router URL, SSH target, GGUF Directory, Preset path, restart command, and optional tokens; no secrets are committed.
- [ ] The picker lists GGUF Quants with quant label and size, groups sharded files into one selectable Quant, and flags detected mmproj/mtp Sidecars.
- [ ] A supplied optional Hub token enables browsing gated repos.
