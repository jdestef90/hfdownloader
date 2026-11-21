# HF Downloader

A self-hosted terminal UI for installing GGUF Quants from the Hugging Face Hub onto a llama.cpp Router.

It takes a Hub URL, lets you pick a Quant, transfers the files into the Router host's GGUF Directory, drafts a Preset with an editable Context Window, and runs a confirmed restart. It never loads a model automatically.

## Requirements

- Node.js 22.18 or newer (the source is TypeScript and runs through Node's built-in type stripping).
- SSH access to the Router host when running in `ssh` mode, with key authentication.
- `cat`, `mkdir`, `mv`, `rm`, `wc`, and either `sha256sum` or `shasum` on the Router host.

## Run

```sh
npm install
./bin/hfdownloader https://huggingface.co/unsloth/Qwen3-0.6B-GGUF
# or, with no URL, paste one into the TUI:
./bin/hfdownloader
```

Use `--help` for all options. `--mode local` overrides the configured mode for one run.

## Configuration

Configuration comes from one JSON file, with environment variables taking precedence. The default path is `$XDG_CONFIG_HOME/hfdownloader/config.json` (usually `~/.config/hfdownloader/config.json`). Copy `config.example.json` there and fill in the values.

| Key | Environment variable | Meaning |
| --- | --- | --- |
| `mode` | `HFDOWNLOADER_MODE` | `ssh` (default) or `local` |
| `routerUrl` | `HFDOWNLOADER_ROUTER_URL` | Router HTTP base URL (default `http://127.0.0.1:8080`) |
| `sshTarget` | `HFDOWNLOADER_SSH_TARGET` | `user@host` for SSH mode |
| `ggufDirectory` | `HFDOWNLOADER_GGUF_DIRECTORY` | Directory on the Router host where Quant files live |
| `presetPath` | `HFDOWNLOADER_PRESET_PATH` | Router Preset INI file on the Router host |
| `restartCommand` | `HFDOWNLOADER_RESTART_COMMAND` | Command that restarts the Router |
| `helperModel` | `HFDOWNLOADER_HELPER_MODEL` | Model id on the Router used for Context Window suggestions |
| `hubToken` | `HFDOWNLOADER_HUB_TOKEN` | Optional Hub token for gated or private repositories |
| `routerApiKey` | `HFDOWNLOADER_ROUTER_API_KEY` | Optional Bearer key for the Router |
| `helperTimeoutMs` | `HFDOWNLOADER_HELPER_TIMEOUT_MS` | Helper request timeout (default `15000`) |

`ggufDirectory`, `presetPath`, and `restartCommand` are interpreted on the Router host in `ssh` mode, and on the local machine in `local` mode. Use absolute paths. `local` mode is for copying the same binary onto the Router host and running it there.

Do not commit real tokens. Keep the config outside the repository, or use the environment variables. `config.local.json`, `.env`, and `dist/` are ignored by git.

## What the flow does

1. **Browse** — loads the repository tree from the Hub, groups sharded files into one Quant, and detects `mmproj` and `mtp` Sidecars.
2. **Download** — streams each file into the GGUF Directory with its original Hub filename. Progress shows bytes, speed, and ETA. Interrupted transfers resume from a `.part` file; cancellation removes partial files.
3. **Verify** — checks the SHA-256 published by the Hub (falling back to the Hub's linked ETag). A mismatch discards the file and stops before Preset creation.
4. **Draft Preset** — pre-fills an alias from the repository and Quant, points `model` at the downloaded file, and offers a Context Window suggested by the configured helper model. The published Hub limit is the fallback. The suggestion is editable and is never written silently.
5. **Write Preset** — supports structured fields and a raw INI view. A timestamped backup is written before any change, and overwriting an existing alias needs confirmation.
6. **Restart** — previews the configured restart command, requires confirmation, and runs it over the same connection used for the Preset write. No model is loaded.

Existing target files require explicit overwrite confirmation. Existing Preset settings can be copied as defaults with `d`.

## Development

```sh
npm test          # node:test suite, including a full Hub-to-restart session
npm run typecheck
npm run build
npm run demo      # scripted local end-to-end session against a fake Hub and Router
```

## Design notes

- [ADR 0001](docs/adr/0001-direct-gguf-directory-downloads.md): files are written directly into the GGUF Directory, not through the Router's HTTP download API.
- [GLOSSARY.md](GLOSSARY.md): domain language.
