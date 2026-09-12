"""Subprocess fixture: original CLI and disk records, no Docker/HTTP calls."""
import argparse
import json
from pathlib import Path
import sys
import time
import os

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle
from test_repeat_cutover import deployment_boundary

p = argparse.ArgumentParser()
p.add_argument('config'); p.add_argument('operation'); p.add_argument('mode'); p.add_argument('control')
a = p.parse_args()
control = Path(a.control)
def wait_file(name):
    deadline = time.monotonic() + 20
    while not (control/name).exists():
        if time.monotonic() > deadline: raise RuntimeError('test control timeout')
        time.sleep(.01)

class Driver(deployment_boundary([], {"old": True, "new": False})):
    def verify_local_engine(self):
        (control/(str(os.getpid())+'.entered')).write_text('entered', encoding='utf-8')
        if a.mode == 'hold': wait_file('release')
    def stop_old(self):
        super().stop_old()
        if a.mode == 'crash-point':
            (control/(str(os.getpid())+'.crash-ready')).write_text('prepared', encoding='utf-8')
            wait_file('release')

if a.mode == 'hold': wait_file('go')
lifecycle.DockerDriver = Driver
config = json.loads(Path(a.config).read_text(encoding='utf-8'))
sys.argv = ['lifecycle.py', a.operation] + ([config['candidate']['head']] if a.operation == 'cutover' else []) + ['--config', a.config]
raise SystemExit(lifecycle.main())
