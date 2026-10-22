// usage: node gen.js <ksy file> <output dir>
const fs = require('fs'), path = require('path'), yaml = require('js-yaml');
const KaitaiStructCompiler = require('kaitai-struct-compiler');
const [ksyPath, outDir] = process.argv.slice(2);
const dir = path.dirname(ksyPath);
const importer = {
  importYaml(name, mode) {
    const f = path.join(dir, name + '.ksy');
    return Promise.resolve(yaml.load(fs.readFileSync(f, 'utf8')));
  },
};
KaitaiStructCompiler.compile('go', yaml.load(fs.readFileSync(ksyPath, 'utf8')), importer, false).then(files => {
  fs.mkdirSync(outDir, { recursive: true });
  const pkg = path.basename(outDir);
  for (const [name, content] of Object.entries(files)) fs.writeFileSync(path.join(outDir, name), 'package ' + pkg + '\n\n' + content);
  console.log('wrote', Object.keys(files).join(', '));
}).catch(e => { console.error('FAILED', e.message || e); process.exit(1); });
