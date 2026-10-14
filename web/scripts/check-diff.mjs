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

// Resolve all $ref references to ensure deterministic comparison
const resolver = jsonSchemaResolver();
const resolvedDoc = resolver.resolve(openapiDoc, {
  baseUri: new URL("api/v1", "https://example.com"),
  fileUriResolver: {
    resolve: async (uri) => {
      if (uri === "" || uri === "#") {
        return "";
      }
      return "";
    },
  },
});

const generatedJsonContent = `${JSON.stringify(resolvedDoc, null, 2)}\n`;
const existingJsonContent = fs.existsSync(openapiJsonPath)
  ? fs.readFileSync(openapiJsonPath, "utf8")
  : "";

if (generatedJsonContent === existingJsonContent) {
  console.log("✓ OpenAPI JSON is up to date");
  process.exit(0);
} else {
  console.error("✗ OpenAPI JSON is out of date");
  console.error(
    "  Regenerate with: cd web && npm run generate or make openapi-generate",
  );
  process.exit(1);
}
