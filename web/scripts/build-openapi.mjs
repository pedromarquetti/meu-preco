import fs from "fs";
import path from "path";
import { parse } from "yaml";

const projectRoot = path.resolve(process.cwd(), "..");
const openapiYamlPath = path.join(projectRoot, "api", "openapi.yaml");
const openapiJsonPath = path.join(projectRoot, "api", "openapi.json");

if (!fs.existsSync(openapiYamlPath)) {
  console.error(`Error: OpenAPI YAML not found at ${openapiYamlPath}`);
  process.exit(1);
}

const yamlContent = fs.readFileSync(openapiYamlPath, "utf8");

const openapiDoc = parse(yamlContent);
const jsonContent = `${JSON.stringify(openapiDoc, null, 2)}\n`;

fs.writeFileSync(openapiJsonPath, jsonContent, "utf8");

console.log("✓ OpenAPI JSON generated successfully");
console.log(`  Source: ${openapiYamlPath}`);
console.log(`  Output: ${openapiJsonPath}`);
console.log(`  Schema count: ${Object.keys(openapiDoc.components?.schemas || {}).length}`);
console.log(`  Path count: ${Object.keys(openapiDoc.paths || {}).length}`);
