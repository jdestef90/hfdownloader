# Direct downloads into the GGUF Directory

GGUF files are written straight into the configured GGUF Directory on the Router host: over SSH when the TUI runs locally, and directly on disk when it runs on that host. We rejected the Router's HTTP `/models` download API because it only writes into its HF cache and cannot target a chosen path, while Presets need to point at files in the GGUF Directory. The trade-off is that this tool owns file transfer, integrity checks, and Preset registration itself.
