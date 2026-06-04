#!/usr/bin/env python3
"""Extract text from a PDF file."""

import sys
from pypdf import PdfReader


def extract(path: str) -> str:
    reader = PdfReader(path)
    return "\n".join(page.extract_text() for page in reader.pages)


if __name__ == "__main__":
    print(extract(sys.argv[1]))
