"""Native process/CLI locking; boot/PID reuse use explicit synthetic OS records."""
import importlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from test_repeat_cutover import configuration


class LockConsumerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = self.root/'operator.json'
        self.config.write_text(json.dumps(configuration(self.root)), encoding='utf-8')
        self.state = self.root/'state'
        self.control = self.root/'control'; self.control.mkdir()
        self.processes = []
        self.addCleanup(self.close_processes)

    def close_processes(self):
        for proc in self.processes:
            if proc.poll() is None: proc.kill()
            proc.communicate(timeout=10)

    def spawn(self, operation='cutover', mode='normal'):
        proc = subprocess.Popen([sys.executable, '-B', str(Path(__file__).with_name('lock_consumer.py')),
                                 str(self.config), operation, mode, str(self.control)],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.processes.append(proc)
        return proc

    def wait_for(self, predicate):
        deadline = time.monotonic()+15
        while not predicate():
            if time.monotonic() > deadline: self.fail('process did not reach expected test boundary')
            time.sleep(.01)

    def crash(self):
        proc = self.spawn(mode='crash-point')
        self.wait_for(lambda: (self.control/(str(proc.pid)+'.crash-ready')).exists())
        record = (self.state/'deployment-record.json').read_bytes()
        self.assertEqual(json.loads(record)['status'], 'PREPARED')
        proc.kill(); proc.communicate(timeout=10)
        self.assertNotEqual(proc.returncode, 0)
        self.assertTrue((self.state/'operator.lock').exists(), 'actual crash must leave the owner record')
        print(json.dumps({'kind':'actual-kill', 'pid':proc.pid, 'native_exit':proc.returncode}))
        return record

    def test_killed_cli_leaves_lock_but_original_rollback_can_recover(self):
        record = self.crash()
        proc = self.spawn('rollback'); out, err = proc.communicate(timeout=15)
        self.assertEqual(proc.returncode, 0, (out, err))
        restored = json.loads((self.state/'deployment-record.json').read_bytes())
        self.assertEqual(restored['status'], 'ROLLED_BACK')
        self.assertEqual(restored['snapshot'], json.loads(record)['snapshot'])
        self.assertFalse((self.state/'operator.lock').exists())

    def test_two_processes_compete_without_deleting_live_owner(self):
        self.compete('cutover')

    def test_two_processes_racing_to_reclaim_crashed_lock_have_one_winner(self):
        self.crash()
        for path in self.control.glob('*.entered'): path.unlink()
        self.compete('rollback')

    def test_live_kernel_owner_wins_even_if_metadata_looks_like_an_old_boot(self):
        first = self.spawn(mode='hold')
        (self.control/'go').write_text('go')
        self.wait_for(lambda: (self.control/(str(first.pid)+'.entered')).exists())
        path = self.state/'operator.lock'; original = path.read_bytes()
        value = json.loads(original); value['system']['boot'] = 'synthetic-previous-boot'
        path.write_text(json.dumps(value), encoding='utf-8')
        changed = path.read_bytes()
        second = self.spawn(); out, err = second.communicate(timeout=15)
        self.assertEqual(second.returncode, 1, (out, err))
        self.assertEqual(path.read_bytes(), changed)
        self.assertEqual(len(list(self.control.glob('*.entered'))), 1)
        path.write_bytes(original)
        (self.control/'release').write_text('release')
        first.communicate(timeout=15)
        self.assertEqual(first.returncode, 0)

    def compete(self, operation):
        first, second = self.spawn(operation, 'hold'), self.spawn(operation, 'hold')
        (self.control/'go').write_text('go')
        self.wait_for(lambda: len(list(self.control.glob('*.entered'))) >= 1)
        owner = (self.state/'operator.lock').read_bytes()
        self.wait_for(lambda: first.poll() is not None or second.poll() is not None)
        loser = first if first.poll() is not None else second
        winner = second if loser is first else first
        self.assertEqual(loser.returncode, 1)
        self.assertIsNone(winner.poll())
        self.assertEqual((self.state/'operator.lock').read_bytes(), owner)
        self.assertEqual(len(list(self.control.glob('*.entered'))), 1)
        (self.control/'release').write_text('release')
        winner.communicate(timeout=15); loser.communicate(timeout=10)
        self.assertEqual(winner.returncode, 0)
        self.assertFalse((self.state/'operator.lock').exists())
        print(json.dumps({'kind':'actual-competition','winner_pid':winner.pid,'winner_exit':winner.returncode,
                          'loser_pid':loser.pid,'loser_exit':loser.returncode}))


class OwnerIdentityTests(unittest.TestCase):
    def setUp(self):
        self.assertTrue((Path(__file__).resolve().parents[1]/'operator_lock.py').is_file(),
                        'boot/process-aware lock implementation is missing')
        self.m = importlib.import_module('operator_lock')
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.state = Path(self.tmp.name)
        self.boot = {'host': 'synthetic-host', 'boot': 'synthetic-boot', 'namespace': 'synthetic-pid-namespace'}
        self.owner = {'schema':'xingmang.operator-lock/v1', 'pid':321, 'process_start':'100',
                      'system':self.boot.copy(), 'token':'a'*32}

    def lockfile(self):
        path=self.state/'operator.lock'
        path.write_text(json.dumps(self.owner), encoding='utf-8')
        return path

    def acquire(self, start='200', boot=None):
        with patch.object(self.m, 'system_identity', return_value=boot or self.boot), \
                patch.object(self.m, 'process_start', return_value=start):
            return self.m.acquire_lock(self.state)

    def test_pid_reuse_and_reboot_records_are_reclaimed(self):
        for label, start, boot in [('pid-reuse', '200', self.boot),
                                   ('reboot', '100', {**self.boot, 'boot':'next-boot'}),
                                   ('dead', None, self.boot)]:
            with self.subTest(label=label):
                path=self.lockfile()
                old_bytes = path.read_bytes()
                def probe(pid): return '999' if pid == os.getpid() else start
                with patch.object(self.m, 'system_identity', return_value=boot), patch.object(self.m, 'process_start', side_effect=probe):
                    try:
                        lock=self.m.acquire_lock(self.state)
                    except OSError:
                        self.fail('proven stale owner was not reclaimed: '+label)
                self.assertNotEqual(json.loads(path.read_text())['token'], self.owner['token'])
                self.assertIn(old_bytes, [p.read_bytes() for p in self.state.glob('operator.lock.stale.*.json')])
                lock.release()
                self.assertFalse(path.exists())
                self.assertTrue((self.state/'operator.guard').is_file(), 'guard inode must never be unlinked')

    def test_live_owner_or_unknown_identity_is_retained(self):
        path=self.lockfile(); before=path.read_bytes()
        with self.assertRaises(OSError): self.acquire('100').release()
        self.assertEqual(path.read_bytes(), before)
        with patch.object(self.m, 'system_identity', return_value=self.boot), \
                patch.object(self.m, 'process_start', side_effect=PermissionError('synthetic access denied')):
            with self.assertRaises(OSError): self.m.acquire_lock(self.state)
        self.assertEqual(path.read_bytes(), before)
        with self.assertRaises(OSError): self.acquire(boot={**self.boot, 'namespace':'other'})
        self.assertEqual(path.read_bytes(), before)

    def test_legacy_pid_malformed_or_hardlinked_lock_is_not_removed(self):
        for raw in (str(os.getpid()).encode(), b'{', b'', b'{}'):
            path=self.state/'operator.lock'; path.write_bytes(raw)
            with self.assertRaises(OSError): self.acquire()
            self.assertEqual(path.read_bytes(), raw)
        path=self.lockfile(); os.link(path, self.state/'alias')
        with self.assertRaises(OSError): self.acquire()
        self.assertEqual(path.read_bytes(), (self.state/'alias').read_bytes())

    def test_release_does_not_unlink_changed_owner(self):
        lock=self.acquire()
        path=self.lockfile(); before=path.read_bytes()
        with self.assertRaises(OSError): lock.release()
        self.assertEqual(path.read_bytes(), before)
        # The failed release still closes the kernel lock so safe recovery is possible.
        lock=self.acquire(); lock.release()

    def test_linux_missing_proc_entry_is_not_proof_of_exit_when_pid_is_live(self):
        with patch.object(self.m.sys, 'platform', 'linux'), \
                patch.object(self.m.Path, 'read_text', side_effect=FileNotFoundError), \
                patch.object(self.m.os, 'kill', return_value=None):
            with self.assertRaises(OSError): self.m.process_start(321)
        with patch.object(self.m.sys, 'platform', 'linux'), \
                patch.object(self.m.Path, 'read_text', side_effect=FileNotFoundError), \
                patch.object(self.m.os, 'kill', side_effect=ProcessLookupError):
            self.assertIsNone(self.m.process_start(321))


if __name__ == '__main__': unittest.main()
