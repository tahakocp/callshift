"use strict";

const samples = {
  openai: { name: "client.ts", code: `const OPENAI = "https://api.openai.com/v1";

export async function summarize(text: string, userId: string) {
  const res = await fetch(\`\${OPENAI}/chat/completions\`, {
    method: "POST",
    headers: { Authorization: \`Bearer \${process.env.OPENAI_API_KEY}\`, "Content-Type": "application/json" },
    body: JSON.stringify({
      model: "gpt-4o-mini",
      messages: [{ role: "user", content: text }],
      max_tokens: 300,
      user: userId,
    }),
  });
  return (await res.json()).choices[0].message.content;
}

// A config object that only mentions the URL is not a request body.
export const settings = { endpoint: "https://api.openai.com/v1/chat/completions", max_tokens: 1024 };
` },
  shop: { name: "orders.py", code: `import requests

API = "https://api.shop.example/v1"

def list_orders(page):
    return requests.get(f"{API}/orders", params={"page": page, "per_page": 50}).json()

def buy(sku):
    body = {"sku": sku, "qty": 1, "coupon": "WELCOME", "shipping_speed": "overnight"}
    return requests.post(API + "/orders", json=body).json()

def refund(order_id):
    return requests.post(f"{API}/orders/{order_id}/refund")
` },
};

const $ = (id) => document.getElementById(id);
let sets = {};
let timer = 0;

function changeSet() {
  if ($("preset").value === "openai") return sets.openai;
  const out = callshiftDiff($("old").value, $("new").value);
  if (typeof out !== "string") throw new Error(out.error);
  return out;
}

function esc(s) {
  return s.replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]));
}

function run() {
  let cs;
  try {
    cs = changeSet();
  } catch (e) {
    $("setinfo").textContent = "Spec error: " + e.message;
    return;
  }
  const parsed = JSON.parse(cs);
  const n = parsed.changes ? parsed.changes.length : 0;
  $("setinfo").textContent = `${parsed.api || "API"}: ${n} change(s), ${parsed.operations.length} operation(s), hosts ${(parsed.hosts || []).join(", ") || "any"}`;
  const out = callshiftScan(cs, $("name").value || "client.ts", $("code").value, $("conf").value);
  if (typeof out !== "string") {
    $("diff").textContent = out.error;
    return;
  }
  const r = JSON.parse(out);
  $("count").textContent = `(${r.findings.length})`;
  $("findings").innerHTML = r.findings.map((f) => `<li>
      <div><span class="sev ${f.severity}">${f.severity}</span> <span class="where">line ${f.line} · ${f.method}${f.method_guessed ? "?" : ""} ${esc(f.path)} · ${f.confidence} confidence</span></div>
      <div>${esc(f.message)}</div></li>`).join("") || "<li class=muted>No affected calls.</li>";
  $("diff").innerHTML = r.diff
    ? r.diff.split("\n").map((l) => l.startsWith("+") && !l.startsWith("+++") ? `<span class=add>${esc(l)}</span>`
        : l.startsWith("-") && !l.startsWith("---") ? `<span class=del>${esc(l)}</span>` : esc(l)).join("\n")
    : "No safe automatic renames for this code.";
}

function schedule() {
  clearTimeout(timer);
  timer = setTimeout(run, 150);
}

function load(preset) {
  $("specs").hidden = preset !== "shop";
  $("name").value = samples[preset].name;
  $("code").value = samples[preset].code;
  run();
}

async function main() {
  const [openai, v1, v2] = await Promise.all(["openai-deprecations.json", "shop-v1.yaml", "shop-v2.yaml"].map((u) => fetch(u).then((r) => r.text())));
  sets.openai = openai;
  $("old").value = v1;
  $("new").value = v2;
  const go = new Go();
  const wasm = await WebAssembly.instantiateStreaming(fetch("callshift.wasm"), go.importObject);
  go.run(wasm.instance);
  $("preset").addEventListener("change", (e) => load(e.target.value));
  for (const id of ["code", "name", "conf", "old", "new"]) $(id).addEventListener("input", schedule);
  load($("preset").value);
}

main().catch((e) => { $("diff").textContent = "Could not start: " + e.message; });
