#!/usr/bin/env python3
import unittest

from node_audience import census


class AudienceCensusTest(unittest.TestCase):
    def test_processing_transport_and_runtime_names(self):
        response = {"clients": {"fields": ["sessid", "tags", "stream"], "data": [
            ["viewer", [], "live+source_fw1r_123456781234423482341234567890ab"],
            ["processing", ["fwproc"], "live+source"],
            ["Iinput", [], "live+source"], ["Ooutput", [], "live+source"],
            ["Uunknown", [], "live+source"], ["viewer", [], "live+source"],
        ]}}
        self.assertEqual(census(response), {"audience": 1, "streams": {"source": 1}})

    def test_explicit_empty_census(self):
        self.assertEqual(census({"clients": {"fields": ["stream", "sessid", "tags"], "data": None}}), {"audience": 0, "streams": {}})

    def test_unavailable_or_malformed_is_never_zero(self):
        for response in ({"clients": {"fields": [[]]}}, {}, {"clients": {}}, {"clients": {"fields": ["stream"]}}, {"clients": {"fields": ["stream", "sessid", "tags"], "data": [["source"]]}}):
            with self.subTest(response=response), self.assertRaises(ValueError):
                census(response)


if __name__ == "__main__":
    unittest.main()
