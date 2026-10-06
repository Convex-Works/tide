// Fails unless src/lib/config.ts documents exactly the TIDE_* variables the
// server reads. The server's names come from server/internal/config/config.go
// (or the path given as the first argument).
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const site = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const configGo = resolve(process.argv[2] ?? resolve(site, '../server/internal/config/config.go'));
const reference = resolve(site, 'src/lib/config.ts');

const names = (text, pattern) => new Set([...text.matchAll(pattern)].map((match) => match[1]));
const server = names(readFileSync(configGo, 'utf8'), /"(TIDE_[A-Z0-9_]+)"/g);
const documented = names(readFileSync(reference, 'utf8'), /name: '(TIDE_[A-Z0-9_]+)'/g);

const missing = [...server].filter((name) => !documented.has(name)).sort();
const stale = [...documented].filter((name) => !server.has(name)).sort();

if (server.size === 0) {
  console.error(`check-config-reference: found no TIDE_* variables in ${configGo}`);
  process.exit(1);
}
if (missing.length > 0 || stale.length > 0) {
  if (missing.length > 0) console.error(`Undocumented in src/lib/config.ts: ${missing.join(', ')}`);
  if (stale.length > 0) console.error(`Documented but not read by the server: ${stale.join(', ')}`);
  process.exit(1);
}
console.log(`check-config-reference: all ${server.size} TIDE_* variables are documented`);
