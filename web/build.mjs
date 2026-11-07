// Builds two self-contained HTML files into dist/: the MCP App view
// (served as ui://substrate-console/console.html) and the local shell.
import { build } from "esbuild";
import { readFile, writeFile, mkdir } from "node:fs/promises";

const version = process.env.VERSION ?? "dev";
const fonts = `<link rel="preconnect" href="https://fonts.googleapis.com"><link rel="preconnect" href="https://fonts.gstatic.com" crossorigin><link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Geist:wght@400;500;600&family=Geist+Mono:wght@400;500&display=swap">`;

async function bundle(entry) {
  const r = await build({
    entryPoints: [entry], bundle: true, minify: true, format: "esm", target: "es2022",
    write: false, outdir: "out", jsx: "automatic", jsxImportSource: "preact",
    define: { __VERSION__: JSON.stringify(version) }, legalComments: "none", logLevel: "warning",
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
