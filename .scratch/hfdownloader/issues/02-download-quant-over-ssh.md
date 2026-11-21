# 02: Download a Quant into the GGUF Directory over SSH

**What to build:** Selecting a Quant streams it into the configured GGUF Directory over SSH with the original Hub filename. Live progress, cancellation with cleanup, resume, and integrity checking make the transfer safe and observable.

**Blocked by:** 01: Browse Quants from a Hub URL

**Status:** ready-for-agent

- [ ] Selecting a Quant streams its file(s) into the configured GGUF Directory over SSH using the original Hub filename.
- [ ] Progress shows bytes transferred and speed/ETA; cancelling removes partial files.
- [ ] An interrupted transfer resumes where the transport allows it.
- [ ] The SHA-256 published by the Hub is checked when the file can be read; a mismatch discards the file and prevents a Preset from being created.
- [ ] An existing target file requires explicit overwrite confirmation.
