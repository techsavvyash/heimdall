import { readFile, readdir } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { JSDOM } from "jsdom";

// Mermaid's parser sanitizes labels through DOMPurify, including in Node.
const dom = new JSDOM("");
globalThis.window = dom.window;
globalThis.document = dom.window.document;
const { default: mermaid } = await import("mermaid");
mermaid.initialize({ startOnLoad: false });
const root = new URL("../content/", import.meta.url);
let count = 0;
let failures = 0;
for (const path of (await readdir(root, { recursive: true })).sort()) {
  if (!path.endsWith(".md")) continue;
  const source = await readFile(new URL(path, root), "utf8");
  for (const match of source.matchAll(/^```mermaid\s*\n([\s\S]*?)^```\s*$/gm)) {
    count++;
    try {
      await mermaid.parse(match[1]);
    } catch (error) {
      failures++;
      const line = source.slice(0, match.index).split("\n").length;
      console.error(`${fileURLToPath(new URL(path, root))}:${line}\n${error.message}`);
    }
  }
}
dom.window.close();
if (!count) throw new Error("No Mermaid diagrams found");
console.log(`Mermaid: ${count} diagrams checked, ${failures} failures`);
if (failures) process.exitCode = 1;
