#!/bin/sh
# Rebuild the two OpenAI change sets used by the evaluation from the
# public openai/openai-openapi repository.
set -eu
cd "$(dirname "$0")"
base=https://raw.githubusercontent.com/openai/openai-openapi
curl -fsSL "$base/e145786e70bf5fc1bc73c7cd19884f445d52c383/openapi.yaml" -o /tmp/openai-2023-11.yaml
curl -fsSL "$base/373f26a9aec3608e3158f2a0d4b8ae49d13162a4/openapi.yaml" -o /tmp/openai-2026-11.yaml
callshift deprecations /tmp/openai-2026-11.yaml -o openai-deprecations.json
callshift diff /tmp/openai-2023-11.yaml /tmp/openai-2026-11.yaml -o openai-2023-11-to-2026-11.json
