"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { byteColumnToUtf16 } = require("../positions");

test("maps ASCII diagnostic columns without adjustment", () => {
  assert.equal(byteColumnToUtf16("id String @id", 4), 3);
});

test("maps UTF-8 byte columns to VS Code UTF-16 offsets", () => {
  assert.equal(byteColumnToUtf16("名字 String @id", 8), 3);
  assert.equal(byteColumnToUtf16("😀id String", 5), 2);
});

test("clamps diagnostic columns past the line", () => {
  assert.equal(byteColumnToUtf16("id", 99), 2);
});
