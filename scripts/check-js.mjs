// Checks the report script beyond what node --check can see. The script is
// one function scope, so two declarations of the same function silently
// leave only the later one: every call to the earlier one goes astray.
import { readFileSync } from "node:fs";

const file = process.argv[2];
const src = readFileSync(file, "utf8");
const seen = new Map();
let bad = false;
for (const m of src.matchAll(/^  function (\w+)\s*\(/gm)) {
  const line = src.slice(0, m.index).split("\n").length;
  if (seen.has(m[1])) {
    console.error(`${file}:${line}: ${m[1]} is already declared at line ${seen.get(m[1])}`);
    bad = true;
  } else {
    seen.set(m[1], line);
  }
}
process.exit(bad ? 1 : 0);
