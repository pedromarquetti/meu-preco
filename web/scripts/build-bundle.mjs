import fs from "fs";
import path from "path";

const projectRoot = path.resolve(process.cwd(), "..");
const webDocsPath = path.join(projectRoot, "web", "docs");
const distWebPath = path.join(projectRoot, "dist");

const openapiYamlPath = path.join(projectRoot, "api", "openapi.yaml");
const openapiJsonPath = path.join(projectRoot, "api", "openapi.json");

if (!fs.existsSync(openapiYamlPath)) {
  console.error(`Error: OpenAPI YAML not found at ${openapiYamlPath}`);
  process.exit(1);
}

if (!fs.existsSync(openapiJsonPath)) {
  console.error(`Error: OpenAPI JSON not found at ${openapiJsonPath}`);
  process.exit(1);
}

// Ensure dist directory exists
const assetsDir = path.join(distWebPath, "assets");
if (!fs.existsSync(assetsDir)) {
  fs.mkdirSync(assetsDir, { recursive: true });
}

// Copy OpenAPI YAML
fs.copyFileSync(openapiYamlPath, path.join(distWebPath, "openapi.yaml"));
console.log("✓ Copied openapi.yaml");

// Copy OpenAPI JSON
fs.copyFileSync(openapiJsonPath, path.join(distWebPath, "openapi.json"));
console.log("✓ Copied openapi.json");

// Copy index.html
const indexPath = path.join(distWebPath, "index.html");
fs.copyFileSync(path.join(webDocsPath, "index.html"), indexPath);
console.log("✓ Copied index.html");

// Copy assets
const assetFiles = ["swagger-ui-bundle.js", "swagger-ui.css"];
for (const file of assetFiles) {
  const srcPath = path.join(webDocsPath, "assets", file);
  const dstPath = path.join(assetsDir, file);
  if (fs.existsSync(srcPath)) {
    fs.copyFileSync(srcPath, dstPath);
    console.log(`✓ Copied ${file}`);
  }
}

console.log("\n✓ Bundle assembled successfully");
console.log(`  Location: ${distWebPath}`);
console.log(`  Files:`);
console.log(`    - index.html`);
console.log(`    - openapi.yaml`);
console.log(`    - openapi.json`);
console.log(`    - assets/swagger-ui-bundle.js`);
console.log(`    - assets/swagger-ui.css`);
