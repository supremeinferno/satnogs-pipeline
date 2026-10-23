// usage: node gen.js <ksy file> <output dir>
const fs = require('fs'), path = require('path'), yaml = require('js-yaml');
const KaitaiStructCompiler = require('kaitai-struct-compiler');
const [ksyPath, outDir] = process.argv.slice(2);
const dir = path.dirname(ksyPath);
// js-yaml turns a key such as 0x6465206861326772 (GRBBeta's switch on the
// first 8 bytes) into a JavaScript number and loses its low digits. Quoted, it
// reaches the compiler as an expression and stays exact.
const load = text => yaml.load(text.replace(/^(\s*)(0x[0-9a-fA-F]{14,}|[0-9]{16,})(\s*:)/gm, "$1'$2'$3"));
const importer = {
  importYaml(name, mode) {
    const f = path.join(dir, name + '.ksy');
    return Promise.resolve(load(fs.readFileSync(f, 'utf8')));
  },
};
KaitaiStructCompiler.compile('go', load(fs.readFileSync(ksyPath, 'utf8')), importer, false).then(files => {
  fs.mkdirSync(outDir, { recursive: true });
  const pkg = path.basename(outDir);
  for (const [name, content] of Object.entries(files)) {
    // The compiler writes such a key as int64(...) even when it switches on a
    // uint64 (a u8 field), which does not compile. Untyped, it fits either.
    const fixed = content.replace(/case int64\(([0-9]{16,})\):/g, 'case $1:');
    fs.writeFileSync(path.join(outDir, name), 'package ' + pkg + '\n\n' + fixed);
  }
  console.log('wrote', Object.keys(files).join(', '));
}).catch(e => { console.error('FAILED', e.message || e); process.exit(1); });
