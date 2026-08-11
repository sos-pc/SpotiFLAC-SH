"""Runtime hooks that wrap upstream functions at import time.

Each hook module self-applies when imported and exposes an `_applied`
flag so the build can verify it without side effects.
"""

