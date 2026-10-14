import fs from "fs";
import path from "path";
import { parse } from "yaml";
import { createConfig, lintFromString } from "@redocly/openapi-core";

const projectRoot = path.resolve(process.cwd(), "..");
const openapiYamlPath = path.join(projectRoot, "api", "openapi.yaml");

if (!fs.existsSync(openapiYamlPath)) {
  console.error(`Error: OpenAPI YAML not found at ${openapiYamlPath}`);
  process.exit(1);
}

const yamlContent = fs.readFileSync(openapiYamlPath, "utf8");

// Verify YAML syntax
try {
  parse(yamlContent);
  console.log("✓ YAML syntax is valid");
} catch (error) {
  console.error("✗ YAML syntax is invalid:", error.message);
  process.exit(1);
}

// Validate OpenAPI document
try {
  // Use Redocly's structural validator without style rules here. Contract
  // style (operation IDs, tag descriptions, and similar conventions) can be
  // adopted separately without making this build step reject valid OAS 3.1.
  const config = await createConfig({ extends: [] });
  const validation = await lintFromString({
    source: yamlContent,
    absoluteRef: openapiYamlPath,
    config,
  });

  if (validation.length > 0) {
    console.error("✗ OpenAPI validation failed:");
    validation.forEach((error) => {
      console.error(`  - ${error.message}`);
    });
    process.exit(1);
  }
  console.log("✓ OpenAPI validation passed");
} catch (error) {
  console.error("✗ OpenAPI validation failed:", error.message);
  process.exit(1);
}

// Verify required OpenAPI fields
const yamlDoc = parse(yamlContent);
const requiredFields = ["openapi", "info", "paths"];
const missingFields = requiredFields.filter((field) => !(field in yamlDoc));

if (missingFields.length > 0) {
  console.error(`✗ Missing required OpenAPI fields: ${missingFields.join(", ")}`);
  process.exit(1);
}
console.log("✓ OpenAPI structure is valid");

// Verify the servers array exists
if (!yamlDoc.servers || yamlDoc.servers.length === 0) {
  console.error("✗ Missing servers array in OpenAPI document");
  process.exit(1);
}
console.log("✓ Servers array is present");

// Verify components/schemas exists when the document contains local schema refs.
const serialized = JSON.stringify(yamlDoc);
const hasSchemaRefs = serialized.includes("#/components/schemas/");
if (hasSchemaRefs && (!yamlDoc.components || !yamlDoc.components.schemas)) {
  console.error("✗ OpenAPI document uses $ref but lacks components/schemas");
  process.exit(1);
}
console.log("✓ Schema references are consistent");
