// The map of Lanternfall: a hill town whose lanterns went out one night.
// Twelve places on a four-by-three grid. Neighbours are the places beside,
// above and below.

export interface Place {
  id: string;
  name: string;
  icon: string;
  description: string;
  /** An item lying hidden here when the game starts. */
  hidden?: string;
}

export const COLUMNS = 4;

export const PLACES: Place[] = [
  { id: "gate", name: "Lantern Gate", icon: "🏮", description: "The town gate. Its great lantern hangs cold, and someone has scratched a crescent into the post." },
  { id: "market", name: "Market Square", icon: "🧺", description: "Empty stalls under striped awnings. A cart of unlit candles has tipped over." },
  { id: "clocktower", name: "Clocktower", icon: "🕰️", description: "The clock stopped at a quarter past midnight. The door to the stair is ajar." },
  { id: "archive", name: "Old Archive", icon: "📜", description: "Shelves of town ledgers. The newest is missing its last page.", hidden: "a torn ledger page" },
  { id: "well", name: "Willow Well", icon: "🪣", description: "A deep well under a willow. The rope is new, and wet." },
  { id: "lane", name: "Glassblower's Lane", icon: "🫙", description: "Rows of empty lantern glass, every one cracked the same way." },
  { id: "forge", name: "Ember Forge", icon: "⚒️", description: "The only warm place in town. The smith is gone, the bellows still sighing." },
  { id: "garden", name: "Moth Garden", icon: "🦋", description: "Night moths circle a single flower that glows faintly blue." },
  { id: "pier", name: "Fisher's Pier", icon: "🎣", description: "Boats tied up for the night. One is missing from its mooring." },
  { id: "chapel", name: "Drowned Chapel", icon: "⛪", description: "Half sunk in the marsh. Candle stubs line the steps above the waterline." },
  { id: "caves", name: "Salt Caves", icon: "🕳️", description: "White tunnels that hum when the wind blows through them." },
  { id: "lighthouse", name: "Lighthouse", icon: "🗼", description: "The tallest light in Lanternfall, dark for the first time in a hundred years." },
];

export function placeById(id: string): Place | undefined {
  return PLACES.find((p) => p.id === id);
}

export function neighbours(id: string): string[] {
  const i = PLACES.findIndex((p) => p.id === id);
  if (i < 0) return [];
  const row = Math.floor(i / COLUMNS);
  const col = i % COLUMNS;
  const out: string[] = [];
  const at = (r: number, c: number) => {
    if (r >= 0 && c >= 0 && c < COLUMNS && r * COLUMNS + c < PLACES.length) out.push(PLACES[r * COLUMNS + c].id);
  };
  at(row - 1, col);
  at(row + 1, col);
  at(row, col - 1);
  at(row, col + 1);
  return out;
}
