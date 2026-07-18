import importlib.util
import json
import os
import tempfile
import unittest


SCRIPT = os.path.join(os.path.dirname(__file__), "source_map_locator.py")
SPEC = importlib.util.spec_from_file_location("source_map_locator", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class SourceMapLocatorTest(unittest.TestCase):
    def test_maps_to_nearest_preceding_segment(self):
        value = {"version": 3, "sources": ["webpack:///src/search.ts"], "names": ["first", "searchUsers"], "mappings": "AAAA,UACEC", "sourcesContent": ["export const searchUsers = () => {}"]}
        result = MODULE.locate(value, 1, 12)
        self.assertEqual(result["original"]["source"], "webpack:///src/search.ts")
        self.assertEqual(result["original"]["line"], 2)
        self.assertEqual(result["original"]["column"], 3)
        self.assertEqual(result["original"]["name"], "searchUsers")
        self.assertTrue(result["original"]["has_source_content"])

    def test_rejects_unmapped_position_and_indexed_map(self):
        with self.assertRaises(MODULE.SourceMapError):
            MODULE.locate({"version": 3, "sources": ["a.ts"], "mappings": "UAAA"}, 1, 1)
        with tempfile.NamedTemporaryFile("w", suffix=".map", delete=False) as handle:
            json.dump({"version": 3, "sources": [], "mappings": "", "sections": []}, handle)
            path = handle.name
        try:
            with self.assertRaises(MODULE.SourceMapError):
                MODULE.load_source_map(path)
        finally:
            os.unlink(path)


if __name__ == "__main__":
    unittest.main()
