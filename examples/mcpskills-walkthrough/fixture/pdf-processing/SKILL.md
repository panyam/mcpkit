---
name: pdf-processing
description: Extract text and form fields from PDF documents
---

# PDF processing

Use `pypdf` for text extraction. For form fields see `references/FORMS.md`.

## Quick start

```python
from pypdf import PdfReader
reader = PdfReader("input.pdf")
text = "\n".join(page.extract_text() for page in reader.pages)
```
