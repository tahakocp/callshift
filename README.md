# callshift

**An API changed. callshift finds the lines in your code that call the parts that changed, and fixes the renames it can prove safe.**

Give it two versions of an OpenAPI or Swagger spec, a single spec with deprecations, or a small change-set file an API provider publishes. It scans your client code in more than a dozen languages, matches each HTTP call to an operation in the spec, and reports the call sites that send a removed parameter, a renamed field, a value that is no longer allowed, or that hit an endpoint that moved or is gone.

[**Try it in the browser**](https://tahakocp.github.io/callshift/) (runs locally in the tab, nothing is uploaded) · [Evaluation on 115 real repositories](eval/)

```
$ callshift scan --old testdata/shop-v1.yaml --new testdata/shop-v2.yaml examples/orders.py
examples/orders.py:6:65: query parameter `per_page` is now `page_size` (same type, similar name) [breaking, medium confidence] GET /orders
    return requests.get(f"{API}/orders", params={"page": page, "per_page": 50}).json()
    fix: per_page -> page_size
examples/orders.py:9:26: request field `qty` was removed from POST /orders [breaking, medium confidence] POST /orders
    body = {"sku": sku, "qty": 1, "coupon": "WELCOME", "shipping_speed": "overnight"}
examples/orders.py:9:36: request field `coupon` is now `coupon_code` (same type, similar name) [breaking, medium confidence] POST /orders
    body = {"sku": sku, "qty": 1, "coupon": "WELCOME", "shipping_speed": "overnight"}
    fix: coupon -> coupon_code
examples/orders.py:9:75: `overnight` is no longer an allowed value of request field `shipping_speed` [breaking, medium confidence] POST /orders
    body = {"sku": sku, "qty": 1, "coupon": "WELCOME", "shipping_speed": "overnight"}
examples/orders.py:10:33: POST /orders now requires request field `currency` [breaking, medium confidence] POST /orders
    return requests.post(API + "/orders", json=body).json()
examples/orders.py:13:33: POST /orders/{id}/refund moved to /orders/{id}/refunds [breaking, high confidence] POST /orders/{id}/refund
    return requests.post(f"{API}/orders/{order_id}/refund")
    fix: refund -> refunds
6 finding(s), 3 with a safe fix; 1 file(s), 0.0 MB in 0 ms
```

The example is [examples/orders.py](examples/orders.py) against two versions of a small [test API](testdata/). Add `--diff` to see the three renames as a patch, or `--fix` to apply them.

## Why

Dependabot keeps your *libraries* current. Nothing does the same for the HTTP APIs you call directly. When a provider renames a field or retires an endpoint, the changelog says what changed, but finding every place in your code that depends on it is manual: search for the field name, sort out which hits are request bodies for that API and which are unrelated, fix them one by one.

[oasdiff](https://github.com/oasdiff/oasdiff) and similar tools tell the *provider* whether a new spec breaks clients. callshift answers the *consumer's* question: which of my calls break, on which lines. It reads code the way a reviewer would. It follows a URL into the call that uses it, the options object or keyword arguments next to it, and the variable or helper function that builds the request body. Then it checks those keys against the operation the URL points to.

Y Combinator's [Fall 2026 Requests for Startups](https://www.ycombinator.com/rfs) describes "Self-Maintaining APIs", software that keeps integrations working as the APIs they call change. callshift is an open, local building block for that idea: the part that turns a spec change into a list of affected lines.

## Install

Binaries for Linux, macOS and Windows are on the [releases page](https://github.com/tahakocp/callshift/releases). With Go 1.23 or newer:

```sh
go install github.com/tahakocp/callshift/cmd/callshift@latest
```

## Use

```sh
# What changed between two versions of a spec?
callshift diff old.yaml new.yaml                  # human-readable
callshift diff old.yaml new.yaml -o changes.json  # portable change set

# Which of my calls are affected?
callshift scan --old old.yaml --new new.yaml src/
callshift scan --spec openapi.yaml .              # use of anything the spec marks deprecated
callshift scan --changes changes.json .           # a change set published by the provider

# Fix what is safe to fix
callshift scan --changes changes.json --diff .    # print a unified diff
callshift scan --changes changes.json --fix .     # apply it
```

Output formats: `--format text` (default), `json`, `md` (for pull request comments), `sarif` (for GitHub code scanning, with fixes attached). Exit status is 1 when findings reach `--fail-on` (`breaking` by default, or `deprecated`, or `none`), 2 on errors.

### What it reports

| Change in the spec | Reported at |
|---|---|
| Operation removed | every call to it |
| Operation moved (same `operationId`, new path) | every call, with a fix when one literal path segment changed |
| Operation deprecated | every call to it |
| Query, header or path parameter removed, renamed, newly required, deprecated | the key in the call, or the call when a required one is missing |
| Request body field removed, renamed, newly required, deprecated, type changed | the key in the body, or the call when a required one is missing |
| Enum value removed | the line that sends that value |
| Response field removed or renamed | lines after the call that read that name (low confidence) |

Renames come from two places: deprecation text such as "Deprecated in favor of `max_completion_tokens`", and pairs of removed and added fields with similar names and the same type. A rename is only applied automatically when the old and new field have the same shape. `max_tokens` to `max_completion_tokens` is fixed; OpenAI's `functions` to `tools` is reported but not touched, because a tool is an object wrapping a function, not a function.

### Confidence

Each finding has a confidence, and `--min-confidence` (default `medium`) hides weaker ones.

- **high**: the URL names the API's host, or a route like `"GET /orders"`, or base path plus a literal segment, and the key is inside the call.
- **medium**: the URL is relative or partly built from variables, or the key is in a variable, helper function or struct that the call uses.
- **low**: the key is a common word found near the call but not provably in it, a sibling of the URL in a settings object, or a response field read.

### Languages

JavaScript and TypeScript, Python, Go, Java, Kotlin, C#, Swift, Rust, PHP, Ruby, Dart, C and C++, shell scripts and YAML (CI files with `curl`). Each has a small lexer that understands its strings, comments and interpolation: template literals, f-strings, `"#{}"`, `$"{}"`, `"\()"` and raw strings. Shell heredocs are not supported yet. Request keys are found in object and map literals, keyword arguments, `put("key", ...)` calls, Kotlin `to` pairs, JSON inside strings, and Go struct tags.

### For API providers: publish a change set

`callshift diff` writes a `callshift/changes/1` JSON file: the hosts and base paths of the API, the list of operations, and the changes. It is small (a few kB for a typical release, against megabytes for a full spec) and enough for clients to scan. Publishing one per release next to the changelog lets every client run:

```sh
curl -fsSLO https://api.example.com/changes/2026-11.json
callshift scan --changes 2026-11.json .
```

Fields: `kind`, `severity` (`breaking` or `deprecated`), `method`, `path`, `in` (`query`, `header`, `path`, `body`, `response`), `name` (dotted, `[]` for array items), and where they apply `new_name`, `new_path`, `values`, `hint`, `old_type`/`new_type`, `safe_rename`.

## GitHub Action

```yaml
- uses: tahakocp/callshift@v0.1.0
  with:
    old: specs/payments-2025.yaml
    new: specs/payments-2026.yaml
    path: src
    fail-on: breaking
    sarif: true        # writes callshift.sarif
- uses: github/codeql-action/upload-sarif@v3
  if: always()
  with:
    sarif_file: callshift.sarif
```

The action also writes a Markdown table to the job summary and sets the output `findings`.

## Numbers

All measured on v0.1.0, on an 8-core Intel Xeon VM.

**Accuracy on real code.** 115 files from 115 public repositories that call OpenAI's chat completions endpoint, scanned for the deprecated `max_tokens` field. 56 lines really send it. Details, labels and scripts are in [eval/](eval/).

| | flagged | correct | precision | recall |
|---|---:|---:|---:|---:|
| callshift (default, medium) | 44 | 44 | 100% | 78.6% |
| callshift, high only | 27 | 27 | 100% | 48.2% |
| text search for `max_tokens` as a key | 107 | 54 | 50.5% | 96.4% |

The text search finds ten more true lines, at the cost of 53 false ones: other providers' APIs, function parameters, local variables. The twelve lines callshift misses all reach the API through data flow it does not follow, such as a URL stored in a struct field or picked from a table of providers. They are listed in [eval/](eval/README.md#what-callshift-missed-at-the-default-confidence-and-why).

Scanning the same files against the full diff of OpenAI's spec from November 2023 to November 2026 (8 breaking changes, 11 deprecations) gives 48 findings, all correct on manual review: the 44 above, two retired `text-davinci` models, a deprecated `response_format` and a deprecated `user` field.

**Speed.**

| | files | size | 8 cores | 1 core |
|---|---:|---:|---:|---:|
| [microsoft/vscode](https://github.com/microsoft/vscode) at `679e1ca` | 17,177 | 239 MB | 3.1 s | 9.6 s |
| [langchain-ai/langchain](https://github.com/langchain-ai/langchain) at `fb10639` | 2,743 | 15 MB | 0.22 s | 0.53 s |
| the 115-file corpus | 115 | 1.25 MB | 0.03 s | |

Diffing two versions of OpenAI's 4.3 MB spec (388 operations) takes about 0.25 s.

The vscode scan with OpenAI's deprecations reports two `max_tokens` lines. Both send to local OpenAI-compatible model servers whose base URL is a variable, so they are right about the request shape but not about the server. Use `--exclude` or a host in the spec to narrow this.

## Limits

- **No cross-file data flow.** URLs and bodies are followed through variables, same-file helper functions and Go structs, not across files, through class fields or through dependency injection.
- **SDK calls are not scanned.** callshift looks for HTTP calls with a URL. `client.chat.completions.create(max_tokens=...)` in an official SDK is not matched to the spec, yet.
- **Heuristic parsing.** The lexers know strings, comments and brackets, not full grammars. Unusual code can be missed or misread; the confidence levels exist for that.
- **Specs.** OpenAPI 3.0, 3.1 and Swagger 2.0, local `$ref` only. External references are not resolved. `oneOf` and `anyOf` fields are treated as optional.
- **The evaluation covers one API and one kind of change.** Numbers on your code base and API will differ.

## Develop

```sh
go test ./...
GOOS=js GOARCH=wasm go build -o site/callshift.wasm ./cmd/wasm   # browser demo
```

MIT licensed.
