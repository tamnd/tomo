"""The Settings facade. A distractor file for retrieval: it references
load_settings but is not where the fix belongs."""

from conflib.loaders import load_settings


class Settings(dict):
    def load(self, source):
        return load_settings(self, source)

    def configure(self, sources):
        for source in sources:
            self.load(source)
        return self
