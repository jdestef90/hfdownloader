# 05: Suggest a Context Window over HTTP

**What to build:** The selected Quant's Hub metadata drives a direct HTTP chat-completions request to the Router using a configured helper model; the suggested Context Window is offered as an editable prefill in the Preset draft, with a metadata fallback when the helper fails.

**Blocked by:** 04: Create or update a Preset for the downloaded Quant

**Status:** ready-for-agent

- [ ] A configured helper model is used for a direct HTTP chat-completions request built from the selected Quant's Hub metadata.
- [ ] The response appears as an editable Context Window prefill in the Preset draft and is never applied silently.
- [ ] On timeout or unusable output, the published Hub context limit is used as a fallback and remains editable.
