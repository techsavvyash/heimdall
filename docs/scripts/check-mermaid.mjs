import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";

// Mermaid's parser sanitizes labels through DOMPurify, including in Node.
const dom = new JSDOM("");
globalThis.window = dom.window;
globalThis.document = dom.window.document;
const { default: mermaid } = await import("mermaid");
mermaid.initialize({ startOnLoad: false, securityLevel: "loose" });
const built = process.argv.includes("--built");
const root = new URL(built ? "../public/" : "../content/", import.meta.url);
let count = 0;
let failures = 0;
async function check(source, location) {
  count++;
  try {
    await mermaid.parse(source);
  } catch (error) {
    failures++;
    console.error(`${location}\n${error.message}`);
  }
}

for (const path of (await readdir(root, { recursive: true })).sort()) {
  if (!path.endsWith(built ? ".html" : ".md")) continue;
  const url = new URL(path, root);
  const source = await readFile(url, "utf8");
  const location = fileURLToPath(url);
  if (built) {
    if (!source.includes('<code class="mermaid"')) continue;
    const page = new JSDOM(source);
    const diagrams = page.window.document.querySelectorAll("code.mermaid");
    for (const [index, diagram] of [...diagrams].entries()) {
      await check(diagram.textContent, `${location} (diagram ${index + 1})`);
    }
    page.window.close();
    continue;
  }
  for (const match of source.matchAll(/^```mermaid\s*\n([\s\S]*?)^```\s*$/gm)) {
    const line = source.slice(0, match.index).split("\n").length;
    await check(match[1], `${location}:${line}`);
  }
}
dom.window.close();
if (!count) throw new Error("No Mermaid diagrams found");
console.log(`Mermaid (${built ? "generated HTML" : "Markdown"}): ${count} diagrams checked, ${failures} failures`);
if (failures) process.exitCode = 1;
