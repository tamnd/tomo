"""Plugin 3: a distractor loader with the same shape as the real one but a
different name, so retrieval must find the right symbol among many look-alikes."""

from conflib.utils import current_env, file_exists, import_module, parse_file, public_names


def register_3(store, source):
    """Load plugin 3 data into the store from a file or module path."""
    if "/" in source:
        base = parse_file(source)
        for key, value in base.items():
            store[key] = value
        env = current_env(store)
        extra = f"{env.lower()}_3_{source}"
        if file_exists(extra):
            for key, value in parse_file(extra).items():
                store[key] = value
        return store
    module = import_module(source)
    for key in public_names(module):
        store[key] = getattr(module, key)
    return store


def describe_3():
    return "plugin 3 loader, unrelated to settings loading"
