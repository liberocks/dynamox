"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { complete } = require("../completion");

function labels(text, beforeCursor = text) {
  return complete(text, beforeCursor.length).map((item) => item.label);
}

test("suggests declarations at the top level", () => {
  assert.deepEqual(labels(""), ["model", "type", "enum"]);
});

test("suggests scalar and named types for model fields", () => {
  const text = "type Metadata {\n  label String\n}\nenum Status { READY }\nmodel User {\n  profile ";
  const suggestions = labels(text);
  assert.ok(suggestions.includes("String"));
  assert.ok(suggestions.includes("Metadata"));
  assert.ok(suggestions.includes("Status"));
});

test("inserts relation syntax when completing a model type", () => {
  const text = "model User {\n id String @id\n @@table(name: \"users\")\n}\nmodel Post {\n id String @id\n userId String\n user ";
  const relation = complete(text, text.length).find((item) => item.label === "User");
  assert.equal(relation.kind, "snippet");
  assert.match(relation.insertText, /@relation\(fields:/);
});

test("suggests model attributes and key fields", () => {
  const text = "model User {\n id String @id\n title String\n @@index(";
  const argumentsList = labels(text);
  assert.ok(argumentsList.includes("partition:"));
  assert.ok(argumentsList.includes("sort:"));
  const fieldList = labels(text + "partition: ");
  assert.ok(fieldList.includes("id"));
  assert.ok(fieldList.includes("title"));
});

test("suggests TTL syntax and model fields", () => {
  const declaration = labels("model Session {\n id String @id\n expiresAt Int?\n @@");
  assert.ok(declaration.includes("@@ttl"));
  const args = labels("model Session {\n id String @id\n expiresAt Int?\n @@ttl(");
  assert.deepEqual(args, ["field:"]);
  const fields = labels("model Session {\n id String @id\n expiresAt Int?\n @@ttl(field: ");
  assert.ok(fields.includes("expiresAt"));
  assert.ok(!fields.includes("id"));
});

test("suggests throughput arguments for tables and GSIs, but not LSIs", () => {
  const table = labels("model Item {\n id String @id\n @@table(");
  assert.ok(table.includes("read:"));
  assert.ok(table.includes("write:"));
  const gsi = labels("model Item {\n id String @id\n @@index(type: GSI, ");
  assert.ok(gsi.includes("read:"));
  assert.ok(gsi.includes("write:"));
  const lsi = labels("model Item {\n id String @id\n @@index(type: LSI, ");
  assert.ok(!lsi.includes("read:"));
  assert.ok(!lsi.includes("write:"));
});

test("suggests attribute arguments and related names", () => {
  const text = "model User {\n id String @id\n @@table(name: \"users\")\n}\nmodel Post {\n id String @id\n userId String\n user User @relation(fields: [userId], references: ";
  const suggestions = labels(text);
  assert.ok(suggestions.includes("id"));
});

test("suggests default generators and does not complete comments", () => {
  const text = "model User {\n id String @id @default(";
  const stringGenerators = labels(text);
  assert.ok(stringGenerators.includes("uuidv7"));
  assert.ok(!stringGenerators.includes("serial"));
  const integerGenerators = labels("model Item {\n id Int @id @default(");
  assert.deepEqual(integerGenerators, ["serial"]);
  assert.deepEqual(labels("// model "), []);
});

test("suggests field attributes and relation arguments", () => {
  const fieldAttributeSuggestions = labels("model User {\n id String @id @");
  assert.ok(fieldAttributeSuggestions.includes("@id"));
  assert.ok(fieldAttributeSuggestions.includes("@default"));
  assert.ok(!fieldAttributeSuggestions.includes("@relation"));
  const relationAttributeSuggestions = labels("model User {\n id String @id\n user User @");
  assert.deepEqual(relationAttributeSuggestions, ["@relation"]);
  const jsonAttributeSuggestions = labels("type Preferences {\n name String\n}\nmodel User {\n id String @id\n preferences Preferences @");
  assert.deepEqual(jsonAttributeSuggestions, ["@jsonString"]);
  const relationArguments = labels("model User {\n id String @id\n user User @relation(");
  assert.ok(relationArguments.includes("fields:"));
  assert.ok(relationArguments.includes("references:"));
  assert.ok(relationArguments.includes("index:"));
});

test("suggests secondary-index types", () => {
  const suggestions = labels("model User {\n id String @id\n @@index(type: ");
  assert.ok(suggestions.includes("GSI"));
  assert.ok(suggestions.includes("LSI"));
});
