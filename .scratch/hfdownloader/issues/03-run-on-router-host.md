# 03: Run the TUI directly on the Router host

**What to build:** A mode switch makes the same binary write and verify files directly on the Router host instead of using SSH, so it can be copied there and run without any remote shell access.

**Blocked by:** 02: Download a Quant into the GGUF Directory over SSH

**Status:** ready-for-agent

- [ ] A mode setting selects direct file writes and verification on the Router host without SSH.
- [ ] Quant selection, progress, cancellation, resume, and integrity checking behave the same as in SSH mode.
- [ ] Switching mode does not change the configured GGUF Directory or the filename used for a Quant.
