"""Loading configuration into a settings store from a file or a module path."""

from conflib.utils import (
    current_env,
    file_exists,
    import_module,
    parse_file,
    public_names,
)


def load_settings(store, source):
    """Load configuration from ``source`` into ``store`` and return ``store``.

    ``source`` is either a filesystem path to a settings file or a dotted Python
    module path. In both cases the base values are loaded first, and then an
    environment-named companion is layered on top when it exists, so a value
    defined for the active environment overrides the base value.
    """
    if is_module_path(source):
        module = import_module(source)
        for key in public_names(module):
            store[key] = getattr(module, key)
        return store

    base = parse_file(source)
    for key, value in base.items():
        store[key] = value
    env = current_env(store)
    companion = f"{env.lower()}_{source}"
    if file_exists(companion):
        for key, value in parse_file(companion).items():
            store[key] = value
    return store


def is_module_path(source):
    """A dotted path with no path separator and no file extension is a module."""
    return "/" not in source and not source.endswith((".toml", ".yaml", ".json"))
