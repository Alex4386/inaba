#!/usr/bin/env node
// Writes THIRD_PARTY_NOTICES.txt: the licenses of everything inaba's binaries carry. Run it
// with `make notices` after changing dependencies; it needs web/node_modules installed.
//
// Go modules are those linked into either binary on any system released. Web packages are
// those the bundle's source maps name, and the ones that ship only styles or fonts.
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const web = path.join(root, "web");
const isNotice = /^(un)?licen[sc]e|^copying|^notice|^third-?party-?notices|\.license\.txt$/i;

// What source maps do not show: stylesheets and fonts.
const styles = ["@fontsource-variable/geist", "tailwindcss", "tw-animate-css", "@tailwindcss/typography", "shadcn"];

// Code and pictures kept in this repository, and what a license asks to be said.
const copied = [
  { name: "shadcn/ui (components in web/src/components/ui)", license: "MIT", url: "https://github.com/shadcn-ui/ui", text: "Copyright (c) 2023 shadcn", mit: true },
  { name: "ReUI (components in web/src/components/reui)", license: "MIT", url: "https://github.com/keenthemes/reui", text: "Copyright (c) 2025 Keenthemes Inc", mit: true },
  { name: "Simple Icons (system marks in web/src/components/host-icon.tsx)", license: "CC0-1.0", url: "https://github.com/simple-icons/simple-icons", text: "Dedicated to the public domain under CC0 1.0. The marks remain trademarks of their owners." },
  { name: "Google APIs (generated code from google/api/*.proto)", license: "Apache-2.0", url: "https://github.com/googleapis/googleapis", text: "Copyright Google LLC. The Apache License 2.0 is reproduced below." },
];
const remarks = {
  elkjs: "Dual-licensed EPL-2.0 OR GPL-3.0-or-later; inaba uses it under EPL-2.0. Its source: https://github.com/kieler/elkjs and https://github.com/eclipse-elk/elk",
  dompurify: "Dual-licensed MPL-2.0 OR Apache-2.0; inaba uses it under Apache-2.0.",
  "github.com/hashicorp/yamux": "MPL-2.0, used unmodified. Its source: https://github.com/hashicorp/yamux",
};

const read = (f) => fs.readFileSync(f, "utf8").replace(/\r\n/g, "\n").replace(/[ \t]+$/gm, "").trim();
function noticesIn(dir) {
  return fs.readdirSync(dir, { withFileTypes: true })
    .filter((e) => e.isFile() && isNotice.test(e.name) && !/\.(go|js|ts|json)$/.test(e.name))
    .map((e) => e.name).sort()
    .map((n) => read(path.join(dir, n))).filter(Boolean);
}

function goModules() {
  const found = new Map();
  for (const [GOOS, GOARCH] of [["linux", "amd64"], ["linux", "arm64"], ["darwin", "arm64"], ["windows", "amd64"], ["windows", "arm64"]]) {
    const out = execFileSync("go", ["list", "-deps", "-f", "{{with .Module}}{{.Path}}|{{.Version}}|{{.Dir}}{{end}}|{{.Dir}}", "./cmd/inaba", "./cmd/inaba-server"], {
      cwd: root, encoding: "utf8", maxBuffer: 1 << 26, env: { ...process.env, GOOS, GOARCH, CGO_ENABLED: "0" },
    });
    for (const line of out.split("\n")) {
      const [name, version, dir, pkg] = line.split("|");
      if (!pkg || name === "github.com/Alex4386/inaba") continue;
      const m = found.get(name) ?? { name, version, dir, texts: new Set() };
      found.set(name, m);
      // A package may carry a license of its own below the module's.
      for (let d = pkg; d.startsWith(dir); d = path.dirname(d)) {
        for (const t of noticesIn(d)) m.texts.add(t);
        if (d === dir) break;
      }
    }
  }
  const goroot = execFileSync("go", ["env", "GOROOT"], { cwd: root, encoding: "utf8" }).trim();
  const version = execFileSync("go", ["env", "GOVERSION"], { cwd: root, encoding: "utf8" }).trim();
  found.set("", { name: "The Go standard library and runtime", version, texts: new Set([read(path.join(goroot, "LICENSE"))]) });
  return [...found.values()].map((m) => ({ ...m, url: m.dir ? `https://pkg.go.dev/${m.name}` : "https://go.dev" }));
}

function webPackages() {
  const out = fs.mkdtempSync(path.join(os.tmpdir(), "inaba-notices-"));
  const dirs = new Set();
  try {
    execFileSync(path.join(web, "node_modules/.bin/vite"), ["build", "--outDir", out, "--emptyOutDir", "--sourcemap", "--logLevel", "error"], { cwd: web, stdio: ["ignore", "ignore", "inherit"] });
    for (const f of fs.readdirSync(out, { recursive: true })) {
      if (!String(f).endsWith(".map")) continue;
      for (const s of JSON.parse(fs.readFileSync(path.join(out, String(f)), "utf8")).sources) {
        const m = s.match(/node_modules\/\.pnpm\/.*node_modules\/((?:@[^/]+\/)?[^/]+)/);
        if (m) dirs.add(path.join(web, m[0]));
      }
    }
  } finally {
    fs.rmSync(out, { recursive: true, force: true });
  }
  for (const s of styles) dirs.add(fs.realpathSync(path.join(web, "node_modules", s)));
  const found = new Map();
  for (const dir of dirs) {
    const p = JSON.parse(fs.readFileSync(path.join(dir, "package.json"), "utf8"));
    const key = `${p.name}@${p.version}`;
    const author = typeof p.author === "string" ? p.author : p.author?.name;
    const repo = typeof p.repository === "string" ? p.repository : p.repository?.url;
    found.set(key, { name: p.name, version: p.version, license: p.license, dir, author, url: `https://www.npmjs.com/package/${p.name}`, repo, texts: new Set(noticesIn(dir)) });
  }
  return [...found.values()];
}

const mit = read(path.join(root, "LICENSE")).split("\n").slice(4).join("\n");
const byName = (a, b) => (a.name < b.name ? -1 : a.name > b.name ? 1 : 0);
const go = goModules().sort(byName);
const npm = webPackages().sort(byName);

// The Apache License, as one of the modules has it.
const apache = [...go.find((m) => m.name === "github.com/spf13/cobra").texts][0];
const missing = [];
// Part of it is Google's, under a BSD license its files carry at their top.
const protobuf = npm.find((p) => p.name === "@bufbuild/protobuf");
if (protobuf) {
  const head = read(path.join(protobuf.dir, "dist/esm/wire/varint.js")).match(/^\/\/ Copyright 2008[\s\S]*?(?=\n[^/])/m);
  if (!head) throw new Error("@bufbuild/protobuf: its BSD notice is not where it was");
  protobuf.texts.add(apache).add(head[0].replace(/^\/\/ ?/gm, "").trim());
}

// Each text once, under the names it covers.
const texts = new Map();
const entry = (m) => {
  const lines = [`${m.name}${m.version ? ` ${m.version}` : ""}${m.license ? ` (${m.license})` : ""}`, `  ${m.url}`];
  if (remarks[m.name]) lines.push(`  ${remarks[m.name]}`);
  if (m.texts.size === 0) {
    // No file of its own: what its package.json says has to do.
    const who = `Copyright (c) ${m.author ?? `the authors of ${m.name}`}`;
    if (/^\(?MIT/.test(m.license ?? "")) m.texts.add(`${who}\n\n${mit}`);
    else if (/^\(?Apache-2\.0/i.test(m.license ?? "")) m.texts.add(apache);
    // These ask for no notice to be kept.
    else if (m.license === "Unlicense" || m.license === "0BSD") m.texts.add(`${m.license}: https://spdx.org/licenses/${m.license}.html`);
    else throw new Error(`${m.name}: no license text found (${m.license}); add it by hand`);
    missing.push(`${m.name} (${m.license})`);
  }
  for (const t of m.texts) texts.set(t, [...(texts.get(t) ?? []), m.name]);
  return lines.join("\n");
};
const list = (title, items) => `${title}\n${"-".repeat(title.length)}\n\n${items.join("\n")}\n`;
const doc = [
  "inaba is under the MIT license (see LICENSE). It carries the work of others, listed here,\neach under its own license. The license texts follow the lists.\n",
  list("Go modules", go.map(entry)),
  list("Web packages", npm.map(entry)),
  list("In this repository", copied.map((c) => `${c.name} (${c.license})\n  ${c.url}${c.text ? `\n  ${c.text}` : ""}`)),
  "=".repeat(78) + "\nLicense texts\n" + "=".repeat(78) + "\n",
  `For: shadcn/ui, ReUI\n\n${mit}\n`,
];
for (const [text, names] of texts) doc.push(`${"-".repeat(78)}\nFor: ${[...new Set(names)].join(", ")}\n\n${text}\n`);
if (![...texts.keys()].some((t) => /Apache License\s+Version 2\.0/.test(t))) throw new Error("the Apache License is missing");
fs.writeFileSync(path.join(root, "THIRD_PARTY_NOTICES.txt"), doc.join("\n"));
if (missing.length) console.log(`no file of their own, so the standard text: ${missing.join(", ")}`);
console.log(`${go.length} Go modules, ${npm.length} web packages, ${texts.size} texts`);
