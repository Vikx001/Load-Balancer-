#!/usr/bin/env python3
"""Simple Anthropic/Claude API key validator.

This script verifies that an Anthropic API key is valid by calling a lightweight
read-only endpoint. It supports both `x-api-key` and `Authorization: Bearer` headers
to be compatible with different Anthropic deployments.

Usage:
  ANTHROPIC_API_KEY=sk-... python tools/check_claude_key.py
  python tools/check_claude_key.py --key sk-...

Notes:
- The script attempts a small GET request to `https://api.anthropic.com/v1/models`.
- No model completion is invoked, so token consumption is negligible.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import urllib.request
import urllib.error
from typing import Tuple


DEFAULT_ENDPOINT = "https://api.anthropic.com/v1/models"


def try_header(endpoint: str, key: str, header_name: str) -> Tuple[bool, int, str]:
    headers = {}
    if header_name.lower() == "authorization":
        headers["Authorization"] = f"Bearer {key}"
    else:
        headers[header_name] = key
    req = urllib.request.Request(endpoint, headers=headers, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            data = resp.read().decode("utf-8", errors="ignore")
            return True, resp.getcode(), data
    except urllib.error.HTTPError as e:
        try:
            body = e.read().decode("utf-8", errors="ignore")
        except Exception:
            body = ""
        return False, e.code, body
    except Exception as e:
        return False, 0, str(e)


def test_key(key: str, endpoint: str = DEFAULT_ENDPOINT) -> int:
    # Try common header styles
    for header in ("x-api-key", "Authorization"):
        ok, status, body = try_header(endpoint, key, header)
        if ok:
            print("OK: API key appears valid (header used:", header, ")")
            try:
                parsed = json.loads(body)
                # Attempt to show available models if returned
                if isinstance(parsed, dict) and "data" in parsed:
                    print("Models (truncated):", list(parsed.get("data")[:5]))
            except Exception:
                pass
            return 0
        else:
            if status in (401, 403):
                # definitive rejection — try next header style
                print(f"Header {header!s} rejected: HTTP {status}")
            else:
                # network / other issue — report and exit
                print(f"Header {header!s} failed: status={status} body={body!r}")
    print("API key did not validate with the Anthropic endpoint. Check key and network.")
    return 2


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description="Validate Anthropic / Claude API key")
    p.add_argument("--key", "-k", help="API key to test (falls back to ANTHROPIC_API_KEY env var)")
    p.add_argument("--endpoint", help="Endpoint to call (default model-list)", default=DEFAULT_ENDPOINT)
    args = p.parse_args(argv)

    key = args.key or os.environ.get("ANTHROPIC_API_KEY") or os.environ.get("CLAUDE_API_KEY")
    if not key:
        print("No API key provided. Set ANTHROPIC_API_KEY or pass --key.", file=sys.stderr)
        return 3

    return test_key(key, endpoint=args.endpoint)


if __name__ == "__main__":
    raise SystemExit(main())
