import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import numpy as np

import benchmark


class BenchmarkTests(unittest.TestCase):
    def test_cache_snapshot_detects_content_and_timestamp_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "empty").mkdir()
            path = root / "model.bin"
            path.write_bytes(b"old")
            first = benchmark.cache_snapshot(root)
            stamp = path.stat().st_mtime_ns
            path.write_bytes(b"new")
            os.utime(path, ns=(stamp, stamp))
            self.assertNotEqual(first, benchmark.cache_snapshot(root))
            second = benchmark.cache_snapshot(root)
            os.utime(path, ns=(stamp + 1_000_000, stamp + 1_000_000))
            self.assertNotEqual(second, benchmark.cache_snapshot(root))
            self.assertEqual(len(first), 1)

    def test_json_result_refuses_overwrite(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "result.json"
            benchmark.write_json(path, {"结果": True})
            with self.assertRaises(FileExistsError):
                benchmark.write_json(path, {"结果": False})
            self.assertEqual(json.loads(path.read_text()), {"结果": True})

    def test_comparison_reports_error_and_rejects_invalid_heatmaps(self):
        reference = np.zeros((1, 4, 128, 128), dtype=np.float32)
        self.assertTrue(benchmark.compare_outputs(reference, reference)[
            "allclose_rtol_1e-3_atol_1e-4"])
        different = reference.copy()
        different[0, 0, 0, 0] = 0.1
        self.assertFalse(benchmark.compare_outputs(reference, different)[
            "allclose_rtol_1e-3_atol_1e-4"])
        for bad in [np.zeros((1,)), np.full_like(reference, np.nan),
                    np.full_like(reference, -1), np.full_like(reference, 2)]:
            with self.subTest(shape=bad.shape, value=bad.flat[0]):
                with self.assertRaises(ValueError):
                    benchmark.compare_outputs(reference, bad)

    def test_nonpositive_run_count_is_rejected(self):
        result = subprocess.run([sys.executable, str(Path(benchmark.__file__)), "--runs", "0"],
                                capture_output=True, text=True, check=False)
        self.assertEqual(result.returncode, 2)
        self.assertIn("must be positive", result.stderr)


if __name__ == "__main__":
    unittest.main()
