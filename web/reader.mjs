// This entry point deliberately has no transport factory, live URL or credential input.
import corpus from "./contract/observations.json" with { type: "json" };
import { inventoryModel, historyModel } from "./reader-model.mjs";
import { renderInventory, renderHistory } from "./reader-render.mjs";
const fixture = (name) => corpus.valid.find((item) => item.name === name);
const inventory = fixture("mixed_inventory");
const model = await inventoryModel(
  JSON.stringify(inventory.body),
  inventory.request,
);
const cards = document.querySelector("#cards"),
  details = document.querySelector("#details");
document.querySelector("#observed").textContent =
  `Illustrative observation: ${model.observedAt} • ${model.stale ? "Expired fixture snapshot" : "Fixture snapshot"} • No live connection`;
async function select(card) {
  details.replaceChildren();
  if (card.key !== "card_task") {
    const message = document.createElement("p");
    message.textContent =
      "No checkpoint-history fixture is supplied for this bench. No history was inferred.";
    details.append(message);
    return;
  }
  const history = fixture("checkpoints_response");
  const view = await historyModel(
    JSON.stringify(history.body),
    history.request,
  );
  renderHistory(details, card, view);
}
renderInventory(cards, model, select);
await select(model.cards[0]);
