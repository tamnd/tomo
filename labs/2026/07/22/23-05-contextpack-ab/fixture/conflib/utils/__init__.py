"""Helpers used by the loaders. The bodies are stubs for the fixture."""

import importlib


def import_module(dotted):
    """Import and return a module object for a dotted module path."""
    return importlib.import_module(dotted)


def public_names(module):
    """Yield the public attribute names defined on a module."""
    return [name for name in dir(module) if not name.startswith("_")]


def parse_file(path):
    """Parse a settings file into a flat dict. Stubbed for the fixture."""
    return {}


def file_exists(path):
    """Report whether a settings file or companion exists on disk."""
    import os

    return os.path.exists(path)


def current_env(store):
    """Return the active environment name recorded on the store."""
    return store.get("current_env", "development")
