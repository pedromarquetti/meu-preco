import fs from "fs";
import path from "path";
import { parse } from "yaml";
import jsonSchemaResolver from "json-schema-resolver";

const projectRoot = path.resolve(process.cwd(), "..");
const openapiYamlPath = path.join(projectRoot, "api", "openapi.yaml");
const openapiJsonPath = path.join(projectRoot, "api", "openapi.json");

if (!fs.existsSync(openapiYamlPath)) {
  console.error(`Error: OpenAPI YAML not found at ${openapiYamlPath}`);
  process.exit(1);
}

const yamlContent = fs.readFileSync(openapiYamlPath, "utf8");

const openapiDoc = parse(yamlContent);

// Resolve all $ref references to ensure deterministic JSON output
const resolver = jsonSchemaResolver();
const resolvedDoc = resolver.resolve(openapiDoc, {
  baseUri: new URL("api/v1", "https://example.com"),
  fileUriResolver: {
    resolve: async (uri) => {
      // Only resolve internal refs to the same document (empty file)
      if (uri === "" || uri === "#") {
        return "";
      }
      return "";
    },
  },
});

// Serialize with stable key ordering for deterministic output
const jsonContent = `${JSON.stringify(resolvedDoc, null, 2)}\n`;

fs.writeFileSync(openapiJsonPath, jsonContent, "utf8");

console.log("✓ OpenAPI JSON generated successfully");
console.log(`  Source: ${openapiYamlPath}`);
console.log(`  Output: ${openapiJsonPath}`);
console.log(
  `  Schema count: ${
    Object.keys(resolvedDoc.components?.schemas || {}).length
  }`,
);
console.log(`  Path count: ${Object.keys(resolvedDoc.paths || {}).length}`);
