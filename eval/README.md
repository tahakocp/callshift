# Evaluation

How well does `callshift scan` find real call sites that use a deprecated field, compared with a plain text search?

## Corpus

115 source files from 115 public GitHub repositories, found with GitHub code search for the literal `api.openai.com/v1/chat/completions`. The sample has 14 files each of TypeScript, JavaScript, Python, Go, Java, PHP, Ruby and shell, plus two zsh scripts and one `.mjs` file, with one file per repository. The files total 1.25 MB. Kotlin, C#, Swift, Rust and Dart are supported by the scanner but not in this sample.

`manifest.json` lists repository, path and git blob SHA for each file. The files are not stored here; `fetch.py` downloads exactly those blobs.

Selection bias: every file contains the full OpenAI URL as a literal. Code that builds the URL from configuration, or uses an SDK, is not represented, so recall on other code bases will differ.

## Labels

`labels.json` holds 109 lines: every line that the regex baseline or any development version of callshift flagged for `max_tokens`. A line is true when `max_tokens` on it is a request body key sent to `POST /chat/completions` on OpenAI, including endpoints that are configurable but default to OpenAI. Lines for other providers, legacy `/completions`, local variables, function parameters and stored settings are false. 56 lines are true.

Labels were assigned by reading each file. Lines found by neither method are not labelled, so recall is measured against the union of both methods' true hits.

## Change sets

`specs.sh` builds them from [openai/openai-openapi](https://github.com/openai/openai-openapi):

- `openai-deprecations.json`: everything the spec at commit `373f26a` (24 Nov 2026) marks deprecated. 47 entries.
- `openai-2023-11-to-2026-11.json`: the diff from commit `e145786` (spec of 1 Nov 2023, version 2.0.0) to `373f26a` (version 2.3.0). 8 breaking changes, 11 deprecations.

## Run it

```sh
go install github.com/tahakocp/callshift/cmd/callshift@latest
GITHUB_TOKEN=... python3 eval/fetch.py
python3 eval/score.py callshift
```

## Results (v0.1.0)

| | flagged | correct | precision | recall |
|---|---:|---:|---:|---:|
| callshift, `--min-confidence high` | 27 | 27 | 100% | 48.2% |
| callshift, `--min-confidence medium` (default) | 44 | 44 | 100% | 78.6% |
| callshift, `--min-confidence low` | 45 | 44 | 97.8% | 78.6% |
| regex `max_tokens\s*[:=,]` and similar | 107 | 54 | 50.5% | 96.4% |

The regex finds more true lines because it does not need to connect a key to a call. Half of what it flags is something else: other providers' APIs, function parameters, local variables, settings objects.

With the full 2023 to 2026 diff, callshift reports 48 findings at the default confidence: the same 44 `max_tokens` lines plus four others, all checked by hand and correct (two removed `text-davinci` models, one deprecated `response_format` on image generation, one deprecated `user`).

### What callshift missed at the default confidence, and why

| File | Why |
|---|---|
| dariubs/goAI openai.go:51, hoophq/hoop openai.go:140 | URL stored in a struct field and used in another method |
| lajosdeme/watchtower intel.go:271, 330; Progerance/launchy server.js:790; antonmedv/howto providers.go:39 | URL picked from a table of providers |
| RezaSi/go-interview-practice ai.go:232 | URL read from a config field |
| Apress/ChatGPT-for-Java ChatGPTClient.java:105; bndtools/bnd ChatImpl.java:36 | body is a Java object serialized elsewhere |
| inaniwaudon/kiroro-bot gpt.ts:60 | body built by the caller of a wrapper function |
| tangredtea/Spring-Blog AIService.java:206 | URL is a Spring `@Value` default |
| remmintan/minefortress generate-changelog.sh:33 | JSON built with `jq` |

All of these need data flow across functions or files, which callshift does not do.
