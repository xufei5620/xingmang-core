"""Run every local unified preflight/rehearsal/cutover/rollback guard test."""
import argparse
from pathlib import Path
import sys
import unittest

REQUIRED = {'test_preflight.py', 'test_operator.py', 'test_restore.py', 'test_smoke.py'}


def run(root):
    folder = root / 'deploy/rehearsal-unified'
    tests = folder / 'tests'
    missing = sorted(name for name in REQUIRED if not (tests / name).is_file())
    if missing:
        raise RuntimeError('Unified operations tests are not fully integrated: ' + ', '.join(missing))
    sys.path.insert(0, str(folder))
    suite = unittest.TestLoader().discover(str(tests), pattern='test_*.py')
    if suite.countTestCases() == 0:
        raise RuntimeError('Unified operations test suite is empty')
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    if not result.wasSuccessful() or result.skipped or result.unexpectedSuccesses:
        raise RuntimeError('Unified operations require complete passing tests, without skips')
    return result.testsRun


if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project-root',type=Path,default=Path(__file__).resolve().parents[2])
    args=parser.parse_args()
    print(f'Unified operations guard suite passed: {run(args.project_root)} tests; no deployment executed.')
