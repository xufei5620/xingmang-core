#!/usr/bin/env python3
"""Preserve old input provenance before staging. No Docker, copy or deployment."""
import argparse
import json
import sys
from lifecycle import OperatorError, capture_old_inputs, read_public_json


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', required=True)
    parser.add_argument('--retained-root', action='append', required=True)
    parser.add_argument('--new-source-root', required=True)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    try:
        descriptor = capture_old_inputs(read_public_json(args.config), args.retained_root, args.new_source_root, args.output)
        print(json.dumps({'status':'INPUTS_CAPTURED_NOT_RUNTIME_VERIFIED', 'input_snapshot':descriptor}))
        return 0
    except (OperatorError, OSError, ValueError, KeyError, TypeError):
        print('old input capture failed; preserve the original release and inspect the reviewed paths', file=sys.stderr)
        return 1


if __name__ == '__main__': raise SystemExit(main())
