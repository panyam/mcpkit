# Screenshots — pdf-server

Placeholder paths referenced from `../README.md`. Capture by running:

```bash
make demo-app EXAMPLE=pdf-server
```

Then drive the prompts in `../README.md` and screenshot the host UI
at the marked moment.

| File | What to capture |
|---|---|
| `01-default-pdf.png` | PDF Server in basic-host with the default arxiv paper rendered in the iframe; tool result panel visible, expanded to show `viewUUID` + `_meta.interactEnabled` |
| `02-highlights.png` | Yellow highlights on every "attention" match on the visible page after the `highlight_text` interact call |
| `03-zoom.png` | PDF rendered at 150% zoom after the `zoom` interact call |
| `04-screenshot-in-result.png` | Tool result panel (expanded) showing the base64-encoded PNG screenshot returned via `submit_page_data` |
| `05-viewer-state.png` | Tool result panel (expanded) showing viewer state JSON — `currentPage`, `zoom`, `displayMode`, `selection` — returned via `submit_viewer_state` |
