/**
 * 轻量 TS 语法自检：不依赖 node_modules，用 Node 内置的 TypeScript 转换能力解析源文件。
 *
 * 用途：装不了依赖（离线 / 受限环境）时，先确认源码没有语法错误，
 * 真正的类型检查仍然要跑 `npm run build`。
 *
 * 用法：node scripts/ts-syntax-check.cjs [glob...]
 *      默认检查 src/**\/*.ts
 */
const { stripTypeScriptTypes } = require('node:module');
const fs = require('fs');
const path = require('path');

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) walk(p, out);
    else if (e.name.endsWith('.ts') && !e.name.endsWith('.d.ts')) out.push(p);
  }
  return out;
}

const args = process.argv.slice(2);
const files = args.length ? args : walk(path.join(__dirname, '..', 'src'));

let bad = 0;
for (const f of files) {
  try {
    // transform 模式才支持装饰器与构造函数参数属性（Angular 组件两者都用）。
    stripTypeScriptTypes(fs.readFileSync(f, 'utf8'), {
      mode: 'transform',
      sourceMap: false,
      sourceUrl: f,
    });
    console.log('OK   ' + path.relative(process.cwd(), f));
  } catch (e) {
    bad++;
    console.error('FAIL ' + f + '\n     ' + String(e.message).split('\n')[0]);
  }
}
console.log(`\n${files.length - bad}/${files.length} files parsed`);
process.exit(bad ? 1 : 0);
