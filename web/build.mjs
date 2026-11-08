// Builds two self-contained HTML files into dist/: the MCP App view
// (served as ui://substrate-console/console.html) and the local shell.
import { build } from "esbuild";
import { readFile, writeFile, mkdir } from "node:fs/promises";
import { dirname, resolve, sep } from "node:path";

const version = process.env.VERSION ?? "dev";
const fonts = `<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin><link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600&family=Geist+Mono:wght@400;500&display=swap">`;

// zod re-exports all ~50 of its error locales as a namespace, and the MCP SDK
// imports zod as a namespace, so esbuild keeps every locale: about 260 KiB of
// the bundle. Nothing here picks a locale, so offer only English.
const englishOnly = {
  name: "zod-english-only",
  setup(b) {
    b.onResolve({ filter: /\/locales\/index\.js$/ }, (a) =>
      a.importer.includes(`${sep}zod${sep}`) ? { path: resolve(dirname(a.importer), "../locales/en.js").replace(/en\.js$/, "index.js"), namespace: "zod-locales" } : undefined);
    b.onLoad({ filter: /.*/, namespace: "zod-locales" }, (a) => ({
      contents: `export { default as en } from ${JSON.stringify(a.path.replace(/index\.js$/, "en.js"))};`,
      resolveDir: dirname(a.path),
    }));
  },
};

async function bundle(entry) {
  const r = await build({
    entryPoints: [entry], bundle: true, minify: true, format: "esm", target: "es2022",
    write: false, outdir: "out", jsx: "automatic", jsxImportSource: "preact",
    define: { __VERSION__: JSON.stringify(version) }, legalComments: "none", logLevel: "warning", plugins: [englishOnly],
  });
  const js = r.outputFiles.find((f) => f.path.endsWith(".js"))?.text ?? "";
  const css = r.outputFiles.find((f) => f.path.endsWith(".css"))?.text ?? "";
  return { js, css };
}

// String.replace with a function avoids "$&"-style patterns in minified code.
const fill = (tpl, map) => Object.entries(map).reduce((s, [k, v]) => s.replace(k, () => v), tpl);
const safe = (js) => js.replaceAll("</script", "<\\/script");

await mkdir("dist", { recursive: true });
const view = await bundle("src/view/main.tsx");
await writeFile("dist/console.html", fill(await readFile("src/view/index.html", "utf8"), { "<!--FONTS-->": fonts, "/*CSS*/": view.css, "/*JS*/": safe(view.js) }));
const shell = await bundle("src/shell/main.ts");
await writeFile("dist/shell.html", fill(await readFile("src/shell/index.html", "utf8"), { "/*JS*/": safe(shell.js) }));
for (const f of ["console.html", "shell.html"]) {
  const s = (await readFile(`dist/${f}`)).length;
  console.log(`dist/${f}  ${(s / 1024).toFixed(0)} KiB`);
}
