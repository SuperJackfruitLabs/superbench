// Disabled candidate reader: validates proposed data only, never opens a transport.
import requestSchema from "./contract/observation-request.v1.json" with { type: "json" };
import responseSchema from "./contract/observation-response.v1.json" with { type: "json" };
import { decodeObservation, observationErrors } from "./contract/validate.mjs";
function matches(schema, value, root) {
  if (schema.$ref) {
    const definition = root.$defs?.[schema.$ref.split("/").at(-1)];
    return !!definition && matches(definition, value, root);
  }
  if (
    schema.oneOf &&
    schema.oneOf.filter((candidate) => matches(candidate, value, root))
      .length !== 1
  )
    return false;
  if (Object.hasOwn(schema, "const") && value !== schema.const) return false;
  if (schema.enum && !schema.enum.includes(value)) return false;
  if (schema.type === "object") {
    if (value === null || typeof value !== "object" || Array.isArray(value))
      return false;
    if (schema.required?.some((key) => !Object.hasOwn(value, key)))
      return false;
    return Object.entries(value).every(([key, entry]) =>
      Object.hasOwn(schema.properties ?? {}, key)
        ? matches(schema.properties[key], entry, root)
        : schema.additionalProperties !== false,
    );
  }
  if (schema.type === "array")
    return (
      Array.isArray(value) &&
      value.length <= (schema.maxItems ?? Infinity) &&
      value.every((item) => matches(schema.items, item, root))
    );
  if (schema.type === "string")
    return (
      typeof value === "string" &&
      Array.from(value).length >= (schema.minLength ?? 0) &&
      Array.from(value).length <= (schema.maxLength ?? Infinity) &&
      (!schema.pattern || new RegExp(schema.pattern, "u").test(value))
    );
  if (schema.type === "integer")
    return (
      Number.isSafeInteger(value) &&
      value >= (schema.minimum ?? 0) &&
      value <= (schema.maximum ?? Number.MAX_SAFE_INTEGER)
    );
  if (schema.type === "boolean") return typeof value === "boolean";
  return true;
}
export async function parseObservation(raw, kind, request, status = 200) {
  const value = decodeObservation(raw, kind);
  const schema = kind === "request" ? requestSchema : responseSchema;
  if (
    !matches(schema, value, schema) ||
    (await observationErrors(kind, value, request, status)).length
  )
    throw new Error("invalid_observation");
  return value;
}
