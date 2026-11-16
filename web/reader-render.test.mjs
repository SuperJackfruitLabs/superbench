import { test } from "node:test";
import assert from "node:assert/strict";
import { renderInventory, renderHistory } from "./reader-render.mjs";
// Minimal DOM operation recorder, not a browser/layout simulation. Dangerous sinks
// throw so a regression from textContent to HTML/link execution fails this test.
class Element {
  constructor(document, tag) {
    this.ownerDocument = document;
    this.tag = tag;
    this.children = [];
    this.events = {};
    this.textContent = "";
  }
  append(...children) {
    this.children.push(...children);
  }
  replaceChildren(...children) {
    this.children = [...children];
  }
  addEventListener(name, fn) {
    this.events[name] = fn;
  }
  set innerHTML(_) {
    throw new Error("unsafe HTML sink");
  }
  set href(_) {
    throw new Error("unexpected navigation");
  }
  set src(_) {
    throw new Error("unexpected resource");
  }
}
const document = { createElement: (tag) => new Element(document, tag) };
const texts = (root) =>
  [root.textContent, ...root.children.flatMap(texts)].filter(Boolean);
test("actual renderer preserves untrusted host/holder/task strings only as text and selection emits no command", () => {
  const hostile = '<img src=x onerror="globalThis.pwned=true">';
  const card = {
    key: hostile,
    availability: "held",
    host: hostile,
    used: "0 bytes",
    quota: "Unavailable",
    allocated: "Unavailable",
    holder: hostile,
    phase: "ready",
    checkpoint: { summary: hostile },
    transferState: "transferred",
    pendingCheckpoint: true,
  };
  const root = new Element(document, "main");
  let chosen;
  renderInventory(root, { cards: [card], complete: true }, (value) => {
    chosen = value;
  });
  assert.equal(root.children.length, 1);
  assert.equal(texts(root).filter((value) => value === hostile).length, 4);
  const button = root.children[0].children[0];
  button.events.click();
  assert.equal(chosen, card);
  assert.equal(globalThis.pwned, undefined);
});
test("history renders only retained evidence and unavailable time without fabricated live controls", () => {
  const root = new Element(document, "section");
  renderHistory(
    root,
    { key: "card" },
    {
      label: "Latest confirmed checkpoint only",
      entries: [
        {
          summary: "abc",
          timeLabel: "Confirmation time unavailable",
          members: [{ name: "a", branch: "card/task", commit: "def" }],
        },
      ],
    },
  );
  assert.ok(texts(root).includes("Confirmation time unavailable"));
  assert.ok(
    texts(root).some((value) =>
      value.includes("not describe uncommitted work"),
    ),
  );
  const tags = (node) => [node.tag, ...node.children.flatMap(tags)];
  assert.ok(!tags(root).includes("button"));
});
