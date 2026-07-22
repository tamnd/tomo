"""Command line entry point. A distractor file for retrieval."""

import sys

from conflib.base import Settings


def main(argv=None):
    argv = argv or sys.argv[1:]
    settings = Settings()
    for source in argv:
        settings.load(source)
    for key, value in sorted(settings.items()):
        print(f"{key}={value}")
    return 0
