/**
 * Schema Utilities
 * Helper functions for working with introspected GraphQL schema
 */

// Type reference from introspection
export interface TypeRef {
  kind?: string;
  name?: string;
  ofType?: TypeRef;
}

// Full type from introspection
export interface FullType {
  kind?: string;
  name?: string;
  description?: string;
  fields?: SchemaField[];
  inputFields?: InputField[];
  interfaces?: TypeRef[];
  enumValues?: EnumValue[];
  possibleTypes?: TypeRef[];
}

// Field from introspection
export interface SchemaField {
  name: string;
  description?: string;
  args?: InputField[];
  type?: TypeRef;
  isDeprecated?: boolean;
  deprecationReason?: string;
}

// Input field/argument from introspection
export interface InputField {
  name: string;
  description?: string;
  type?: TypeRef;
  defaultValue?: string;
}

// Enum value from introspection
export interface EnumValue {
  name: string;
  description?: string;
  isDeprecated?: boolean;
  deprecationReason?: string;
}

// Full schema from introspection
export interface IntrospectedSchema {
  queryType?: { name?: string; fields?: SchemaField[] };
  mutationType?: { name?: string; fields?: SchemaField[] };
  subscriptionType?: { name?: string; fields?: SchemaField[] };
  types?: FullType[];
}

/**
 * Format a type reference to its full string representation
 * e.g., NON_NULL -> LIST -> Stream becomes [Stream!]!
 */
export function formatTypeString(typeRef: TypeRef | undefined): string {
  if (!typeRef) return "unknown";

  // Build the type string recursively
  return formatTypeRecursive(typeRef);
}

function formatTypeRecursive(type: TypeRef): string {
  if (!type) return "unknown";

  if (type.kind === "NON_NULL") {
    const inner = type.ofType ? formatTypeRecursive(type.ofType) : "unknown";
    return `${inner}!`;
  }

  if (type.kind === "LIST") {
    const inner = type.ofType ? formatTypeRecursive(type.ofType) : "unknown";
    return `[${inner}]`;
  }

  // Named type
  return type.name || "unknown";
}

/**
 * Get the base type name, unwrapping all wrappers
 */
export function getBaseTypeName(typeRef: TypeRef | undefined): string {
  if (!typeRef) return "unknown";

  let current: TypeRef | undefined = typeRef;
  while (current?.ofType) {
    current = current.ofType;
  }

  return current?.name || "unknown";
}

/**
 * Check if a type is a scalar type
 */
export function isScalarType(typeName: string): boolean {
  const scalars = [
    "String",
    "Int",
    "Float",
    "Boolean",
    "ID",
    "Time",
    "DateTime",
    "JSON",
  ];
  return scalars.includes(typeName);
}

/**
 * Find a type definition by name in the schema
 */
export function findType(
  schema: IntrospectedSchema,
  typeName: string,
): FullType | undefined {
  return schema.types?.find((t) => t.name === typeName);
}

/**
 * Get all fields from an input type
 */
export function getInputTypeFields(
  schema: IntrospectedSchema,
  typeName: string,
): InputField[] {
  const type = findType(schema, typeName);
  return type?.inputFields || [];
}

/**
 * Get enum values for a type
 */
export function getEnumValues(
  schema: IntrospectedSchema,
  typeName: string,
): EnumValue[] | null {
  const type = findType(schema, typeName);
  if (type?.kind !== "ENUM") return null;
  return type.enumValues || null;
}

/**
 * Get deprecation info for a field
 */
export function getDeprecationInfo(field: SchemaField): {
  isDeprecated: boolean;
  reason?: string;
} {
  return {
    isDeprecated: field.isDeprecated || false,
    reason: field.deprecationReason || undefined,
  };
}

/**
 * Check if a type is an input type
 */
export function isInputType(
  schema: IntrospectedSchema,
  typeName: string,
): boolean {
  const type = findType(schema, typeName);
  return type?.kind === "INPUT_OBJECT";
}

/**
 * Check if a type is an enum
 */
export function isEnumType(
  schema: IntrospectedSchema,
  typeName: string,
): boolean {
  const type = findType(schema, typeName);
  return type?.kind === "ENUM";
}

/**
 * Check if a type is an object type (has fields)
 */
export function isObjectType(
  schema: IntrospectedSchema,
  typeName: string,
): boolean {
  const type = findType(schema, typeName);
  return type?.kind === "OBJECT";
}

/**
 * Get the fields of an object type
 */
export function getObjectTypeFields(
  schema: IntrospectedSchema,
  typeName: string,
): SchemaField[] {
  const type = findType(schema, typeName);
  if (type?.kind !== "OBJECT") return [];
  return type.fields || [];
}

/**
 * Get possible types for a union or interface
 */
export function getPossibleTypes(
  schema: IntrospectedSchema,
  typeName: string,
): string[] {
  const type = findType(schema, typeName);
  if (!type?.possibleTypes) return [];
  return type.possibleTypes.map((t) => t.name).filter((n): n is string => !!n);
}

/**
 * Check if a type is a union type
 */
export function isUnionType(
  schema: IntrospectedSchema,
  typeName: string,
): boolean {
  const type = findType(schema, typeName);
  return type?.kind === "UNION";
}

/**
 * Check if a type is an interface type
 */
export function isInterfaceType(
  schema: IntrospectedSchema,
  typeName: string,
): boolean {
  const type = findType(schema, typeName);
  return type?.kind === "INTERFACE";
}

/**
 * Get a limited set of fields for an object type (for query generation)
 * Returns the most useful fields, limited to avoid overly verbose queries
 */
export function getPreviewFields(
  schema: IntrospectedSchema,
  typeName: string,
  maxFields: number = 8,
): string[] {
  const type = findType(schema, typeName);
  if (type?.kind !== "OBJECT" || !type.fields) return [];

  // Priority fields that should always be included if present
  const priorityFields = [
    "id",
    "name",
    "message",
    "success",
    "status",
    "createdAt",
  ];

  const fields = type.fields
    .filter((f) => !f.name.startsWith("__"))
    .map((f) => f.name);

  // Sort: priority fields first, then alphabetically
  const sorted = fields.sort((a, b) => {
    const aIdx = priorityFields.indexOf(a);
    const bIdx = priorityFields.indexOf(b);
    if (aIdx !== -1 && bIdx !== -1) return aIdx - bIdx;
    if (aIdx !== -1) return -1;
    if (bIdx !== -1) return 1;
    return a.localeCompare(b);
  });

  return sorted.slice(0, maxFields);
}

/**
 * Format a field's argument list for display
 */
export function formatArguments(args: InputField[] | undefined): string {
  if (!args || args.length === 0) return "";

  return args
    .map((arg) => {
      const typeName = formatTypeString(arg.type);
      const defaultVal = arg.defaultValue ? ` = ${arg.defaultValue}` : "";
      return `${arg.name}: ${typeName}${defaultVal}`;
    })
    .join(", ");
}

/**
 * Get a summary of a type for tooltip/preview
 */
export function getTypeSummary(
  schema: IntrospectedSchema,
  typeName: string,
): string {
  const type = findType(schema, typeName);
  if (!type) return "Unknown type";

  switch (type.kind) {
    case "SCALAR":
      return `Scalar type`;
    case "ENUM":
      const values = type.enumValues?.map((v) => v.name).join(" | ") || "";
      return `Enum: ${values}`;
    case "INPUT_OBJECT":
      const inputCount = type.inputFields?.length || 0;
      return `Input type with ${inputCount} field${inputCount !== 1 ? "s" : ""}`;
    case "OBJECT":
      const fieldCount = type.fields?.length || 0;
      return `Object type with ${fieldCount} field${fieldCount !== 1 ? "s" : ""}`;
    case "UNION":
      const unionTypes =
        type.possibleTypes?.map((t) => t.name).join(" | ") || "";
      return `Union: ${unionTypes}`;
    case "INTERFACE":
      const implCount = type.possibleTypes?.length || 0;
      return `Interface with ${implCount} implementation${implCount !== 1 ? "s" : ""}`;
    default:
      return type.description || "Type";
  }
}

/**
 * Check if a field has required arguments
 */
export function hasRequiredArguments(field: SchemaField): boolean {
  return (
    field.args?.some((arg) => {
      return arg.type?.kind === "NON_NULL";
    }) || false
  );
}

/**
 * Get a flat list of all type names in the schema
 */
export function getAllTypeNames(schema: IntrospectedSchema): string[] {
  return (
    schema.types
      ?.map((t) => t.name)
      .filter((n): n is string => !!n && !n.startsWith("__")) || []
  );
}

/**
 * Get all input types in the schema
 */
export function getAllInputTypes(schema: IntrospectedSchema): FullType[] {
  return (
    schema.types?.filter(
      (t) => t.kind === "INPUT_OBJECT" && !t.name?.startsWith("__"),
    ) || []
  );
}

/**
 * Get all enum types in the schema
 */
export function getAllEnumTypes(schema: IntrospectedSchema): FullType[] {
  return (
    schema.types?.filter(
      (t) => t.kind === "ENUM" && !t.name?.startsWith("__"),
    ) || []
  );
}
