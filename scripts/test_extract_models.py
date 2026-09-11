import importlib.util
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location("extract", Path(__file__).with_name("extract-models.py"))
extract = importlib.util.module_from_spec(spec)
spec.loader.exec_module(extract)
FIXTURE = Path(__file__).with_name("fixtures") / "command-code-1.53.1-models.md"

class ExtractTests(unittest.TestCase):
    def setUp(self):
        self.text = FIXTURE.read_text()
        self.catalog = extract.parse_models_md(self.text)

    def test_actual_package(self):
        rows = {r["id"]: r for r in self.catalog}
        self.assertEqual(rows["claude-sonnet-4-6"]["min_plan"], "Pro")
        self.assertEqual(rows["gpt-5.6-luna"]["min_plan"], "Go")
        self.assertEqual(rows["moonshotai/Kimi-K3"]["reasoning_efforts"], ["low", "high", "max"])
        self.assertEqual(rows["zai-org/GLM-5.1"]["context"], 0)

    def test_membership_only_docs(self):
        extra = [("foreign/model", "foreign/model.toml", {"limit": {"output": 100}})]
        models = extract.build_models(self.catalog, extra, [])
        expected = {m["id"] for m in self.catalog if m["min_plan"] == "Go"}
        self.assertEqual({m["id"] for m in models}, expected)
        self.assertNotIn("claude-sonnet-4-6", expected)

    def test_unknown_plan_and_broken_table(self):
        for text in (
            self.text.replace("Go and above", "Ultra"),
            self.text.replace("Min plan", "Tier"),
            self.text.replace("Id (use EXACTLY this)", "Model identifier"),
            self.text.replace("| Kimi K3 |", "| Kimi | K3 |"),
            "",
        ):
            with self.subTest(text=text[:40]), self.assertRaises(ValueError):
                extract.parse_models_md(text)

    def test_duplicate(self):
        row = next(l for l in self.text.splitlines() if l.startswith("| `deepseek/"))
        with self.assertRaises(ValueError):
            extract.parse_models_md(self.text.replace(row, row + "\n" + row))

    def test_matching_no_fuzzy_or_variants(self):
        entries = [("one/model-v4.1", "one/model-v4.1.toml", {}),
                   ("two/model-v4.1", "two/model-v4.1.toml", {})]
        self.assertEqual(extract.match_models_dev(entries, "one/MODEL-v4.1"), entries[0])
        self.assertIsNone(extract.match_models_dev(entries, "unknown/model-v4.1"))
        self.assertIsNone(extract.match_models_dev(entries, "one/model-v41"))
        self.assertIsNone(extract.match_models_dev(entries, "one/model-v4.1:free"))

    def test_metadata_and_context_precedence(self):
        mid = "moonshotai/Kimi-K3"
        meta = [(mid, mid + ".toml", {"limit": {"context": 2000000, "output": 120000},
                                    "modalities": {"input": ["text", "image"]}})]
        m = next(m for m in extract.build_models(self.catalog, meta, []) if m["id"] == mid)
        self.assertEqual(m["context"], 1000000)
        self.assertEqual(m["output"], 120000)
        self.assertTrue(m["vision"])
        self.assertEqual(m["output_source"], "modelsdev")

    def test_invalid_output_fallback(self):
        mid = "moonshotai/Kimi-K3"
        meta = [(mid, mid + ".toml", {"limit": {"output": 99999999}})]
        m = next(m for m in extract.build_models(self.catalog, meta, []) if m["id"] == mid)
        self.assertEqual(m["output_source"], "fallback")
        self.assertLessEqual(m["output"], m["context"])

    def test_drift(self):
        old = [{"id": str(i)} for i in range(40)]
        new = [{"id": str(i)} for i in range(80)]
        with self.assertRaises(ValueError):
            extract.validate_drift(new, old)
        extract.validate_drift(new, old, True)

    def test_units(self):
        self.assertEqual(extract.parse_context("1.05M"), 1050000)
        self.assertEqual(extract.parse_context("262K"), 262000)
        with self.assertRaises(ValueError):
            extract.parse_context("unlimited")

    def test_carried_budget_stays_stable(self):
        old = [{"id": "moonshotai/Kimi-K3", "output": 120000,
                "output_source": "modelsdev", "output_ref": "moonshotai/kimi-k3.toml"}]
        first = extract.build_models(self.catalog, [], old)
        second = extract.build_models(self.catalog, [], first)
        self.assertEqual(first, second)

    def test_column_reordering(self):
        lines = []
        for line in self.text.splitlines():
            if line.startswith("|"):
                row = extract.cells(line)
                row[0], row[1] = row[1], row[0]
                line = "| " + " | ".join(row) + " |"
            lines.append(line)
        self.assertEqual(extract.parse_models_md("\n".join(lines)), self.catalog)

if __name__ == "__main__":
    unittest.main()
