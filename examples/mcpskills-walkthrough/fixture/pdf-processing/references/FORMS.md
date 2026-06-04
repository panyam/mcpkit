# Form field extraction

PDF interactive forms expose their fields via the AcroForm dictionary.

```python
from pypdf import PdfReader
reader = PdfReader("form.pdf")
fields = reader.get_form_text_fields()
```

Returns a `{field_name: value}` dict for every text field.
