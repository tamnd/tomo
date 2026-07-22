"""A base test that references load_settings. The hidden multi-environment
cases that pin the module-path companion behaviour are not shipped here, exactly
as the real eval withholds them."""

from conflib.loaders import load_settings


def test_load_file_source():
    store = {"current_env": "PRODUCTION"}
    load_settings(store, "settings.toml")
    assert isinstance(store, dict)
