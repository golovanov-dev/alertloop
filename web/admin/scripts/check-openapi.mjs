// check-openapi compares the console's API types (src/types.ts) with the
// schemas in api/openapi.yaml and exits 1 on any difference: a field on one
// side only, a different type, nullability or enum, or a field that the spec
// lists as required and the console treats as optional (or the reverse).
//
//   npm run check:openapi [-- <types.ts> <openapi.yaml>]
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { parse } from "yaml";

const here = (p) => fileURLToPath(new URL(p, import.meta.url));
const typesPath = process.argv[2] ?? here("../src/types.ts");
const specPath = process.argv[3] ?? here("../../../api/openapi.yaml");

// Every exported type in types.ts is listed here: interfaces and enums against
// a schema, Page<T> against each list schema, EventAction against the paths.
const schemaOf = {
  EventType: "EventType",
  Severity: "Severity",
  EventState: "EventState",
  DeliveryState: "DeliveryState",
  ChannelType: "ChannelType",
  AlertEvent: "Event",
  DeliveryAttempt: "DeliveryAttempt",
  AttemptLink: "AttemptLink",
  Stats: "Stats",
  Info: "Info",
};
const pages = { EventList: "AlertEvent", DeliveryAttemptList: "DeliveryAttempt" };

const spec = parse(readFileSync(specPath, "utf8"), { merge: true });
const schemas = spec.components.schemas;
const src = ts.createSourceFile(typesPath, readFileSync(typesPath, "utf8"), ts.ScriptTarget.Latest, true);
const decls = new Map();
// `const X = [...] as const`, for types written as (typeof X)[number].
const consts = new Map();
for (const s of src.statements) {
  if ((ts.isInterfaceDeclaration(s) || ts.isTypeAliasDeclaration(s)) && s.modifiers?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword)) {
    decls.set(s.name.text, s);
  }
  if (ts.isVariableStatement(s)) {
    for (const v of s.declarationList.declarations) {
      if (ts.isIdentifier(v.name) && v.initializer) consts.set(v.name.text, v.initializer);
    }
  }
}

const problems = [];
const fail = (where, msg) => problems.push(`${where}: ${msg}`);

// Both sides are reduced to one shape: {kind, values?, of?, ref?, nullable}.
// kind is string | number | boolean | any | enum | map | array | ref.
function fromTS(node, bind = {}) {
  if (ts.isUnionTypeNode(node)) {
    const rest = node.types.filter((t) => !(ts.isLiteralTypeNode(t) && t.literal.kind === ts.SyntaxKind.NullKeyword));
    const nullable = rest.length < node.types.length;
    if (rest.length > 1 && rest.every((t) => ts.isLiteralTypeNode(t) && ts.isStringLiteral(t.literal))) {
      return { kind: "enum", values: rest.map((t) => t.literal.text).sort(), nullable };
    }
    if (rest.length !== 1) return { kind: `union ${node.getText()}`, nullable };
    return { ...fromTS(rest[0], bind), nullable };
  }
  if (ts.isParenthesizedTypeNode(node)) return fromTS(node.type, bind);
  // (typeof X)[number] where X is a `[...] as const` array of string literals.
  if (ts.isIndexedAccessTypeNode(node) && node.indexType.kind === ts.SyntaxKind.NumberKeyword) {
    let q = node.objectType;
    while (ts.isParenthesizedTypeNode(q)) q = q.type;
    let init = ts.isTypeQueryNode(q) ? consts.get(q.exprName.getText()) : undefined;
    if (init && ts.isAsExpression(init) && init.type.getText() === "const") init = init.expression;
    if (init && ts.isArrayLiteralExpression(init) && init.elements.length && init.elements.every(ts.isStringLiteral)) {
      return { kind: "enum", values: init.elements.map((e) => e.text).sort(), nullable: false };
    }
    return { kind: `unsupported ${node.getText()}`, nullable: false };
  }
  if (ts.isArrayTypeNode(node)) return { kind: "array", of: fromTS(node.elementType, bind), nullable: false };
  if (ts.isTypeReferenceNode(node)) {
    const name = node.typeName.getText();
    if (bind[name]) return { kind: "ref", ref: schemaOf[bind[name]] ?? bind[name], nullable: false };
    if (name === "Record") return { kind: "map", of: fromTS(node.typeArguments[1], bind), nullable: false };
    const d = decls.get(name);
    if (d && ts.isTypeAliasDeclaration(d)) return fromTS(d.type, {});
    if (d) return { kind: "ref", ref: schemaOf[name] ?? name, nullable: false };
    return { kind: `unknown type ${name}`, nullable: false };
  }
  const k = { [ts.SyntaxKind.StringKeyword]: "string", [ts.SyntaxKind.NumberKeyword]: "number", [ts.SyntaxKind.BooleanKeyword]: "boolean", [ts.SyntaxKind.UnknownKeyword]: "any" }[node.kind];
  return { kind: k ?? `unsupported ${node.getText()}`, nullable: false };
}

function fromSpec(s) {
  const nullable = !!s.nullable;
  if (s.$ref || (s.allOf?.length === 1 && s.allOf[0].$ref)) {
    const name = (s.$ref ?? s.allOf[0].$ref).split("/").pop();
    const target = schemas[name];
    return target.enum ? { ...fromSpec(target), nullable } : { kind: "ref", ref: name, nullable };
  }
  if (s.enum) return { kind: "enum", values: [...s.enum].sort(), nullable };
  if (s.type === "string" || s.type === "boolean") return { kind: s.type, nullable };
  if (s.type === "integer" || s.type === "number") return { kind: "number", nullable };
  if (s.type === "array") return { kind: "array", of: fromSpec(s.items), nullable };
  if (s.type === "object" && typeof s.additionalProperties === "object") return { kind: "map", of: fromSpec(s.additionalProperties), nullable };
  if (s.type === "object" && !s.properties) return { kind: "any", nullable };
  return { kind: `unsupported ${JSON.stringify(s)}`, nullable };
}

const show = (t) => (t.kind === "enum" ? t.values.join("|") : t.kind === "ref" ? `$ref ${t.ref}` : t.kind === "map" || t.kind === "array" ? `${t.kind} of ${show(t.of)}` : t.kind) + (t.nullable ? " | null" : "");

// TS `unknown` accepts whatever the spec says (payload is free-form JSON).
function same(a, b) {
  if (a.nullable !== b.nullable) return false;
  if (a.kind === "any") return true;
  if (a.kind !== b.kind) return false;
  if (a.kind === "enum") return a.values.join() === b.values.join();
  if (a.kind === "ref") return a.ref === b.ref;
  if (a.kind === "map" || a.kind === "array") return same(a.of, b.of);
  return true;
}

function compareObject(tsName, schemaName, bind = {}) {
  const where = `${tsName} ~ ${schemaName}`;
  const iface = decls.get(tsName);
  const schema = schemas[schemaName];
  if (!schema?.properties) return fail(where, "schema not found or has no properties");
  const required = schema.required ? new Set(schema.required) : null;
  const seen = new Set();
  for (const m of iface.members) {
    const name = m.name.getText();
    seen.add(name);
    const prop = schema.properties[name];
    if (!prop) {
      fail(where, `field ${name} is not in the spec`);
      continue;
    }
    const a = fromTS(m.type, bind);
    const b = fromSpec(prop);
    if (!same(a, b)) fail(where, `field ${name}: console ${show(a)}, spec ${show(b)}`);
    if (required && required.has(name) === !!m.questionToken) {
      fail(where, `field ${name}: ${m.questionToken ? "optional in the console, required in the spec" : "required in the console, not in the spec's required list"}`);
    }
  }
  for (const name of Object.keys(schema.properties)) {
    if (!seen.has(name)) fail(where, `field ${name} is in the spec but not in the console type`);
  }
}

for (const name of decls.keys()) {
  if (!(name in schemaOf) && name !== "Page" && name !== "EventAction") {
    fail(name, "exported from types.ts but not compared; add it to scripts/check-openapi.mjs");
  }
}
for (const [tsName, schemaName] of Object.entries(schemaOf)) {
  const d = decls.get(tsName);
  if (!d) {
    fail(tsName, "listed in scripts/check-openapi.mjs but not exported from types.ts");
  } else if (ts.isInterfaceDeclaration(d)) {
    compareObject(tsName, schemaName);
  } else {
    const a = fromTS(d.type);
    const b = schemas[schemaName] ? fromSpec(schemas[schemaName]) : { kind: "missing schema", nullable: false };
    if (!same(a, b)) fail(`${tsName} ~ ${schemaName}`, `console ${show(a)}, spec ${show(b)}`);
  }
}
const page = decls.get("Page");
for (const [schemaName, item] of Object.entries(pages)) {
  compareObject("Page", schemaName, { [page.typeParameters[0].name.text]: item });
}
const actions = Object.keys(spec.paths)
  .map((p) => /^\/v1\/events\/\{id\}\/([a-z]+)$/.exec(p))
  .filter((m) => m && spec.paths[m.input].post)
  .map((m) => m[1]);
const consoleActions = fromTS(decls.get("EventAction").type);
if (consoleActions.values?.join() !== actions.sort().join()) {
  fail("EventAction ~ POST /v1/events/{id}/…", `console ${show(consoleActions)}, spec ${actions.join("|")}`);
}

if (problems.length) {
  console.error(`The console types differ from ${specPath}:\n  ` + problems.join("\n  "));
  process.exit(1);
}
console.log(`The console types match ${specPath}.`);
