// Copyright 2026 Monutchee
// SPDX-License-Identifier: Apache-2.0
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { rollup } from 'rollup';
import string from '@bkuri/rollup-plugin-string';
import image from '@rollup/plugin-image';
import json from '@rollup/plugin-json';
import { nodeResolve } from '@rollup/plugin-node-resolve';
import terser from '@rollup/plugin-terser';
import url from '@rollup/plugin-url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const source = path.join(root, 'third_party/libapps');
const output = path.join(root, 'internal/webui/static/vendor/hterm');
const revision = '55c860e79cb4a2fb160f7dfd63c00d8120fab24d';
const git = (...args) => execFileSync('git', ['-C', source, ...args], { encoding: 'utf8' }).trim();
if (git('rev-parse', 'HEAD') !== revision) throw new Error('Initialize libapps at the pinned revision: git submodule update --init');
git('diff', '--exit-code', 'HEAD');
const work = await mkdtemp(path.join(tmpdir(), 'station-hterm-'));
try {
  // Build a tracked snapshot in isolation; never write generated files into the submodule.
  execFileSync('tar', ['-x', '-C', work], { input: execFileSync('git', ['-C', source, 'archive', revision, 'hterm', 'libdot', 'nassh/_locales/en/messages.json'], { maxBuffer: 32 << 20 }) });
  await symlink(path.join(here, 'node_modules'), path.join(work, 'node_modules'), 'dir');
  const metadata = { gitCommitHash: revision, gitDate: git('show', '-s', '--format=%cI', revision) };
  for (const library of ['libdot', 'hterm']) {
    const filename = path.join(work, library, 'package.json');
    await writeFile(filename, JSON.stringify({ ...JSON.parse(await readFile(filename)), ...metadata }));
  }
  const bundle = async (input, filename, plugins) => {
    const result = await rollup({ input: path.join(work, input), plugins, onwarn(warning, warn) {
      // Upstream intentionally has cycles between its namespace modules.
      if (warning.code !== 'CIRCULAR_DEPENDENCY') warn(warning);
    } });
    await result.write({ file: path.join(work, filename), format: 'es', indent: false, preferConst: true, esModule: false });
    await result.close();
  };
  await bundle('libdot/js/deps_resources.shim.js', 'libdot/dist/js/libdot_resources.js', [json(), terser()]);
  await bundle('hterm/js/deps_resources.shim.js', 'hterm/dist/js/hterm_resources.js', [json(), string({ include: ['**/*.html', '**/*.svg'] }), image({ exclude: '**/*.svg' }), url({ include: '**/*.ogg', limit: Infinity }), terser()]);
  await bundle('hterm/js/deps_punycode.shim.js', 'hterm/js/deps_punycode.rollup.js', [nodeResolve({ preferBuiltins: false })]);
  await writeFile(path.join(work, 'entry.js'), "export { hterm } from './hterm/index.js';\nexport { lib } from './libdot/index.js';\n");
  await bundle('entry.js', 'hterm.js', [nodeResolve({ preferBuiltins: false })]);
  const files = new Map();
  files.set('hterm.js', await readFile(path.join(work, 'hterm.js')));
  const messages = JSON.parse(await readFile(path.join(work, 'nassh/_locales/en/messages.json')));
  files.set('messages.en.json', JSON.stringify(Object.fromEntries(Object.entries(messages).filter(([key]) => key.startsWith('HTERM_'))), null, 2) + '\n');
  files.set('LICENSE.hterm', await readFile(path.join(work, 'hterm/LICENSE')));
  files.set('LICENSE.libdot', await readFile(path.join(work, 'libdot/LICENSE')));
  files.set('LICENSE.wcwidth', await readFile(path.join(work, 'hterm/third_party/wcwidth/LICENSE.md')));
  files.set('LICENSE.punycode', await readFile(path.join(here, 'node_modules/punycode/LICENSE-MIT.txt')));
  const digest = createHash('sha256').update(files.get('hterm.js')).digest('hex');
  files.set('provenance.json', JSON.stringify({ upstream: 'https://chromium.googlesource.com/apps/libapps', revision, hterm: '1.92.1', libdot: '9.0.0', sha256: digest }, null, 2) + '\n');
  await mkdir(output, { recursive: true });
  for (const [name, data] of files) {
    const filename = path.join(output, name);
    if (process.argv.includes('--check')) {
      if (!Buffer.from(data).equals(await readFile(filename))) throw new Error(`Generated asset differs: ${name}`);
    } else await writeFile(filename, data);
  }
  console.log(`hterm assets ${process.argv.includes('--check') ? 'verified' : 'generated'} (${digest})`);
} finally { await rm(work, { recursive: true, force: true }); }
