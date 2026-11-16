// All contract strings are untrusted task/display data; none become markup or links.
function element(document, tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = String(text);
  if (className) node.className = className;
  return node;
}
function facts(document, values) {
  const list = element(document, "dl");
  for (const [label, value] of values) {
    list.append(element(document, "dt", label), element(document, "dd", value));
  }
  return list;
}
export function renderInventory(root, model, select) {
  const document = root.ownerDocument;
  root.replaceChildren();
  for (const card of model.cards) {
    const article = element(document, "article", undefined, "bench-card");
    const button = element(document, "button", card.key, "bench-name");
    button.type = "button";
    button.addEventListener("click", () => select(card));
    article.append(
      button,
      element(document, "span", card.availability, "state"),
    );
    article.append(
      facts(document, [
        ["Host", card.host],
        ["Used", card.used],
        ["Quota", card.quota],
        ["Allocated", card.allocated],
        ["Current holder", card.holder],
        ["Initialization phase", card.phase],
        ["Last confirmed checkpoint", card.checkpoint.summary],
      ]),
    );
    if (card.pendingCheckpoint)
      article.append(
        element(document, "p", "Checkpoint publication is pending.", "warning"),
      );
    if (card.transferState)
      article.append(
        element(
          document,
          "p",
          `Source transfer: ${card.transferState}. Writing is blocked.`,
          "warning",
        ),
      );
    root.append(article);
  }
  if (!model.cards.length)
    root.append(
      element(
        document,
        "p",
        model.error
          ? "Inventory unavailable"
          : "No benches in this observation.",
      ),
    );
  if (!model.complete)
    root.append(
      element(
        document,
        "p",
        "More entries exist; this fixture does not load additional pages.",
      ),
    );
}
export function renderHistory(root, card, model) {
  const document = root.ownerDocument;
  root.replaceChildren();
  root.append(
    element(document, "h2", card.key),
    element(document, "p", model.label, "history-coverage"),
  );
  const list = element(document, "ol", undefined, "history");
  for (const entry of model.entries) {
    const row = element(document, "li");
    row.append(
      element(document, "p", entry.timeLabel),
      element(document, "code", entry.summary),
    );
    for (const member of entry.members)
      row.append(
        element(
          document,
          "p",
          `${member.name} • ${member.branch} • ${member.commit}`,
          "member",
        ),
      );
    list.append(row);
  }
  root.append(
    list,
    element(
      document,
      "p",
      "A recorded checkpoint does not describe uncommitted work or prove a writer is stopped.",
      "muted",
    ),
  );
}
