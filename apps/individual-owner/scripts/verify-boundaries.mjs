import { readFileSync, readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import assert from 'node:assert/strict';
const owner=fileURLToPath(new URL('../',import.meta.url));
const failures=[];
for(const folder of ['web/src','mobile/lib','shared']) {
  for(const file of readdirSync(path.join(owner,folder),{recursive:true}).filter(p=>/\.(tsx?|dart|json)$/.test(p))) {
    const source=readFileSync(path.join(owner,folder,file),'utf8');
    for(const pattern of [/\bfetch\s*\(/,/\b(?:XMLHttpRequest|WebSocket|Dio)\s*\(/,/https?:\/\//,/postgres(?:ql)?:\/\//,/\b(?:sk-proj-|sk_live_|AKIA)[A-Za-z0-9]+/,/\b(?:pin|access_code)\s*[:=]\s*["']?\d{4,8}\b/i]) {
      if(pattern.test(source)) failures.push(`${folder}/${file}: ${pattern}`);
    }
  }
}
assert.deepEqual(failures,[]);
console.log('PASS: runtime source has no HTTP/provider adapters, remote URLs, database DSNs, credential patterns or access PIN assignments.');
