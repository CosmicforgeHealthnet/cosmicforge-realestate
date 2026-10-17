import assert from 'node:assert/strict';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
const owner = fileURLToPath(new URL('../', import.meta.url));
const galleryRoot = path.join(owner, 'design/stitch');
const read = p => readFileSync(p, 'utf8').replace(/^\uFEFF/, '');
const inventory = JSON.parse(read(path.join(galleryRoot, 'inventory.json')));
assert.equal(inventory.screens.length, 30);
assert.equal(new Set(inventory.screens.map(s=>s.id)).size, 30);
for (const platform of ['DESKTOP','MOBILE']) assert.equal(inventory.screens.filter(s=>s.platform===platform).length, 15);
const sums = read(path.join(galleryRoot, 'SHA256SUMS.txt')).trim().split(/\r?\n/);
assert.equal(sums.length, 63);
for (const sum of sums) {
  const [,hash,relative] = sum.match(/^([a-f0-9]{64})  (.+)$/);
  assert.equal(createHash('sha256').update(readFileSync(path.join(owner, relative))).digest('hex'), hash, relative);
}
assert.equal(inventory.archive.bytes, statSync(path.join(owner, inventory.archive.file)).size);
assert.equal(inventory.archive.sha256, sums[0].slice(0,64));
const gallery = read(path.join(galleryRoot, 'index.html'));
for (const screen of inventory.screens) {
  for (const key of ['html','screenshot']) assert.ok(statSync(path.join(galleryRoot, screen[key])).isFile(), screen[key]);
  assert.ok(gallery.includes(`src="${screen.screenshot}"`));
  assert.ok(gallery.includes(`href="${screen.html}"`));
  const pair = inventory.screens.filter(s=>s.pairId===screen.pairId);
  assert.equal(pair.length,2);
  assert.notEqual(pair[0].platform,pair[1].platform);
}
const files = readdirSync(path.join(galleryRoot,'original'), {recursive:true}).filter(f=>statSync(path.join(galleryRoot,'original',f)).isFile());
for(const [extension,count] of [['.html',30],['.png',30],['.md',2]]) assert.equal(files.filter(f=>f.endsWith(extension)).length,count);
console.log('PASS: ZIP + 62 original hashes; 30 HTML/PNG links; 15 exact desktop/mobile pairs.');
