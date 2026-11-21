# HF Downloader

A self-hosted terminal tool that selects quantized models from Hugging Face and installs them on a remote llama server.

## Language

**Quant**:
A specific quantization of a model published as a GGUF file in a Hugging Face repository, identified by a tag such as `Q4_K_M` or `IQ3_XXS`.
_Avoid_: variant, quantization file, model file

**Router**:
The server that owns model aliases, performs model downloads on request, and serves models to clients.
_Avoid_: llama server, inference server

**Preset**:
A named configuration on the Router that binds a model alias to a Quant and its runtime settings.
_Avoid_: INI section, model entry, config block

**Context Window**:
The maximum number of tokens a model can process in one request, configurable per Preset.
_Avoid_: ctx size, context length

**GGUF Directory**:
The directory on the Router host where Quant files are kept and from which Presets load models.
_Avoid_: models folder, HF cache

**Sidecar**:
A companion GGUF file, such as an `mmproj` or `mtp` file, that a Quant may need alongside its main file.
_Avoid_: auxiliary file, extra model

**Hub**:
The Hugging Face service that hosts GGUF repositories and publishes file sizes and SHA-256 hashes.
_Avoid_: HF, website

**Helper Model**:
A model already available on the Router that the tool asks, over HTTP, for a Context Window suggestion.
_Avoid_: planner model, LLM
