// The markup the vanilla and extras Views share with views/bridge.html, so all
// four Views look alike and the e2e test can use the same selectors.
export interface Color {
  hex: string;
  name: string;
}

export function elements() {
  const q = (id: string) => document.querySelector(`[data-testid=${id}]`) as HTMLElement;
  return {
    swatch: q("swatch"),
    label: q("label"),
    button: q("darker") as HTMLButtonElement,
    status: q("status"),
  };
}

export function render(c: Color) {
  const { swatch, label, button } = elements();
  swatch.style.background = c.hex;
  swatch.dataset.hex = c.hex;
  label.textContent = `${c.name} ${c.hex}`;
  button.disabled = false;
}

export function contextFor(c: Color) {
  return {
    content: [{ type: "text" as const, text: `The user is looking at ${c.name} ${c.hex}` }],
    structuredContent: { hex: c.hex },
  };
}
