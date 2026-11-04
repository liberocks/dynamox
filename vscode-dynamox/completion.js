"use strict";

const scalarTypes = ["String", "Int", "Float", "Boolean", "DateTime", "Bytes"];

function complete(text, offset) {
  const before = text.slice(0, offset);
  const lineStart = before.lastIndexOf("\n") + 1;
  const linePrefix = before.slice(lineStart);
  const trimmed = linePrefix.trimStart();
  if (trimmed.startsWith("//")) return [];

  const masked = maskCommentsAndStrings(text);
  const blocks = getBlocks(masked, text);
  const block = blockAt(blocks, offset);
  const currentFields = block ? fieldsIn(block.body) : [];
  const completions = [];
  const add = (label, insertText = label, detail = "", kind = "keyword") => {
    if (!completions.some((item) => item.label === label)) {
      completions.push({ label, insertText, detail, kind });
    }
  };

  if (!block) {
    for (const keyword of ["model", "type", "enum"]) {
      add(keyword, keyword + " ${1:Name} {\n  ${0}\n}", "Dynamox declaration", "snippet");
    }
    return completions;
  }

  const models = blocks.filter((item) => item.kind === "model");
  const types = blocks.filter((item) => item.kind === "type");
  const enums = blocks.filter((item) => item.kind === "enum");
  const callPrefix = linePrefix;
  const modelName = block.name;

  if (block.kind === "model" && /@@\w*$/.test(trimmed)) {
    add("@@table", "@@table(name: ${1:\"table_name\"})", "Set the DynamoDB table", "snippet");
    add("@@index", "@@index(name: ${1:\"ByField\"}, partition: ${2:field}, type: GSI)", "Declare a global secondary index", "snippet");
    add("@@ttl", "@@ttl(field: ${1:expiresAt})", "Set the DynamoDB TTL field", "snippet");
    return completions;
  }

  const modelCall = callPrefix.match(/@@(table|index|ttl)\(([^)]*)$/);
  if (modelCall) {
    const attr = modelCall[1];
    const args = modelCall[2];
    const currentArg = args.slice(args.lastIndexOf(",") + 1).trim();
    if (attr === "ttl") {
      if (currentArg.startsWith("field:")) {
        for (const field of currentFields) {
          if (field.type === "Int" && !field.list) add(field.name, field.name, "TTL field in " + modelName, "field");
        }
        return completions;
      }
      if (!/(?:^|,)\s*field\s*:/.test(args)) add("field:", "field: ", "TTL argument", "property");
      return completions;
    }
    if (currentArg.startsWith("type:")) {
      add("GSI", "GSI", "Global secondary index", "constant");
      add("LSI", "LSI", "Local secondary index", "constant");
      return completions;
    }
    if (currentArg.startsWith("partition:") || currentArg.startsWith("sort:")) {
      for (const field of currentFields) add(field.name, field.name, "Field in " + modelName, "field");
      return completions;
    }
    const isLocalIndex = attr === "index" && /(?:^|,)\s*type\s*:\s*LSI\b/i.test(args);
    const properties = attr === "table"
      ? ["name: ", "read: ", "write: "]
      : ["name: ", "partition: ", "sort: ", "type: ", ...(isLocalIndex ? [] : ["read: ", "write: "])];
    for (const property of properties) {
      if (!new RegExp(`(?:^|,)\\s*${property.slice(0, -2)}\\s*:`).test(args)) {
        add(property.trim(), property, `${attr} argument`, "property");
      }
    }
    return completions;
  }

  const relationCall = callPrefix.match(/@relation\(([^)]*)$/);
  if (relationCall) {
    const args = relationCall[1];
    const currentArg = args.slice(args.lastIndexOf(",") + 1).trim();
    if (currentArg.startsWith("fields:")) {
      for (const field of currentFields) add(field.name, field.name, "Local model field", "field");
      return completions;
    }
    if (currentArg.startsWith("references:")) {
      const fieldLine = callPrefix.match(/^\s*\w+\s+(\w+)/);
      const target = fieldLine && models.find((item) => item.name === fieldLine[1]);
      for (const field of target ? fieldsIn(target.body) : []) add(field.name, field.name, "Related model field", "field");
      return completions;
    }
    if (currentArg.startsWith("index:")) {
      const fieldLine = callPrefix.match(/^\s*\w+\s+(\w+)/);
      const target = fieldLine && models.find((item) => item.name === fieldLine[1]);
      for (const index of indexesIn(target ? target.sourceBody : "")) add(index, index, "Related model index", "field");
      return completions;
    }
    for (const property of ["fields: ", "references: ", "index: "]) {
      if (!args.includes(property.trim().slice(0, -1) + ":")) add(property.trim(), property, "@relation argument", "property");
    }
    return completions;
  }

  if (/@default\([^)]*$/.test(callPrefix)) {
    const field = callPrefix.match(/^\s*\w+\s+(\w+)/);
    const isPartitionKey = /@(?:id|partitionKey)\b/.test(callPrefix);
    const generators = isPartitionKey && field && field[1] === "Int"
      ? ["serial"]
      : isPartitionKey && field && field[1] === "String"
        ? ["uuidv1", "uuidv4", "uuidv7", "objectid"]
        : [];
    for (const value of generators) add(value, value, "ID generator", "constant");
    return completions;
  }

  if (/(?:^|\s)@\w*$/.test(callPrefix)) {
    const field = callPrefix.match(/^\s*\w+\s+(\w+)/);
    const fieldType = field && field[1];
    const isPartitionKey = /@(?:id|partitionKey)\b/.test(callPrefix);
    let attrs = [];
    if (block.kind === "model" && ["String", "Int", "Float", "DateTime", "Bytes"].includes(fieldType)) {
      attrs = ["@id", "@partitionKey", "@sortKey"];
      if (isPartitionKey && (fieldType === "String" || fieldType === "Int")) attrs.push("@default");
    } else if (block.kind === "model" && models.some((model) => model.name === fieldType)) {
      attrs = ["@relation"];
    } else if (block.kind === "model" && [...types, ...enums].some((type) => type.name === fieldType)) {
      attrs = ["@jsonString"];
    }
    for (const attr of attrs) add(attr, attr, "Field attribute", "attribute");
    return completions;
  }

  if (block.kind === "model" && trimmed === "") {
    add("field", "${1:fieldName} ${2:String}", "Add a model field", "snippet");
    add("@@table", "@@table(name: ${1:\"table_name\"})", "Set the DynamoDB table", "snippet");
    add("@@index", "@@index(name: ${1:\"ByField\"}, partition: ${2:field}, type: GSI)", "Declare a global secondary index", "snippet");
    add("@@ttl", "@@ttl(field: ${1:expiresAt})", "Set the DynamoDB TTL field", "snippet");
    return completions;
  }
  if (block.kind === "type" && trimmed === "") {
    add("field", "${1:fieldName} ${2:String}", "Add a composite type field", "snippet");
    return completions;
  }

  if (block.kind === "model" || block.kind === "type") {
    const typeLine = trimmed.match(/^\w+\s+([\w]*)$/);
    if (typeLine) {
      for (const type of scalarTypes) add(type, type, "Dynamox scalar type", "type");
      for (const type of [...types, ...enums]) {
        add(type.name, type.name, `${type.kind} declared in this schema`, "type");
      }
      if (block.kind === "model") {
        for (const model of models) {
          add(model.name, model.name + " @relation(fields: [${1:localField}], references: [${2:key}])", "Model relation", "snippet");
        }
      }
      return completions;
    }
  }

  // At the start of a model field, offer local field names for key/index arguments.
  if (block.kind === "model" && /(?:partition|sort):\s*\w*$/.test(callPrefix)) {
    for (const field of currentFields) add(field.name, field.name, "Field in " + modelName, "field");
  }
  return completions;
}

function maskCommentsAndStrings(text) {
  const chars = text.split("");
  let inString = false;
  let inComment = false;
  let escaped = false;
  for (let i = 0; i < chars.length; i += 1) {
    const char = chars[i];
    const next = chars[i + 1];
    if (inComment) {
      if (char === "\n") inComment = false;
      else chars[i] = " ";
      continue;
    }
    if (inString) {
      if (char !== "\n") chars[i] = " ";
      if (!escaped && char === '"') inString = false;
      if (!escaped && char === "\\") escaped = true;
      else escaped = false;
      continue;
    }
    if (char === "/" && next === "/") {
      chars[i] = " ";
      chars[i + 1] = " ";
      i += 1;
      inComment = true;
    } else if (char === '"') {
      chars[i] = " ";
      inString = true;
    }
  }
  return chars.join("");
}

function getBlocks(masked, original) {
  const blocks = [];
  const declaration = /\b(model|type|enum)\s+([A-Za-z_]\w*)\s*\{/g;
  let match;
  while ((match = declaration.exec(masked))) {
    const open = masked.indexOf("{", match.index);
    let depth = 1;
    let end = open + 1;
    for (; end < masked.length && depth > 0; end += 1) {
      if (masked[end] === "{") depth += 1;
      if (masked[end] === "}") depth -= 1;
    }
    const close = depth === 0 ? end - 1 : masked.length;
    blocks.push({
      kind: match[1],
      name: match[2],
      open,
      close,
      body: masked.slice(open + 1, close),
      sourceBody: original.slice(open + 1, close)
    });
    declaration.lastIndex = close + 1;
  }
  return blocks;
}

function blockAt(blocks, offset) {
  return blocks.find((block) => offset > block.open && offset <= block.close);
}

function fieldsIn(body) {
  const fields = [];
  for (const line of body.split("\n")) {
    const match = line.match(/^\s*([A-Za-z_]\w*)\s+([A-Za-z_]\w*)(\[\])?/);
    if (match && !match[1].startsWith("@@")) fields.push({ name: match[1], type: match[2], list: Boolean(match[3]) });
  }
  return fields;
}

function indexesIn(body) {
  return [...body.matchAll(/@@index\s*\(\s*name\s*:\s*["']([^"']+)["']/g)].map((match) => match[1]);
}

module.exports = { complete };
