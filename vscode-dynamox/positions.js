"use strict";

function byteColumnToUtf16(lineText, oneBasedByteColumn) {
  const targetByte = Math.max(0, oneBasedByteColumn - 1);
  let byteOffset = 0;
  let utf16Offset = 0;
  for (const character of lineText) {
    if (byteOffset >= targetByte) return utf16Offset;
    byteOffset += Buffer.byteLength(character, "utf8");
    utf16Offset += character.length;
  }
  return utf16Offset;
}

module.exports = { byteColumnToUtf16 };
