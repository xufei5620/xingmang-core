"""Local OS mutex plus public process identity; never infer staleness from age."""
import ctypes
from functools import lru_cache
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import uuid


class LockError(OSError):
    """Only fixed, non-secret reasons are reported."""


def require(value, reason):
    if not value: raise LockError(reason)


def checked_path(path):
    path = Path(path)
    require(path.is_absolute() and '..' not in path.parts, 'lock path must be absolute')
    for part in (path, *path.parents):
        require(not part.is_symlink() and not (hasattr(part, 'is_junction') and part.is_junction()),
                'linked lock path is forbidden')
    return path


def checked_stat(path):
    value = checked_path(path).stat()
    require(stat.S_ISREG(value.st_mode) and value.st_nlink == 1, 'lock must be a single-link regular file')
    return value


@lru_cache(maxsize=1)
def system_identity():
    if sys.platform == 'linux':
        host = Path('/etc/machine-id').read_text(encoding='ascii').strip()
        boot = Path('/proc/sys/kernel/random/boot_id').read_text(encoding='ascii').strip()
        require(re.fullmatch(r'[a-f0-9]{32}', host) and re.fullmatch(r'[a-f0-9-]{36}', boot),
                'host or boot identity is unavailable')
        return {'host': host, 'boot': boot, 'namespace': os.readlink('/proc/self/ns/pid')}
    if os.name == 'nt':
        # Fixed local CIM query; no remote host, environment values or credentials.
        shell = Path(os.environ['SystemRoot'])/'System32/WindowsPowerShell/v1.0/powershell.exe'
        query = "$ErrorActionPreference='Stop'; $v=Get-CimInstance -ClassName Win32_OperatingSystem -Property CSName,LastBootUpTime; @{host=$v.CSName;boot=$v.LastBootUpTime.ToUniversalTime().ToString('o')} | ConvertTo-Json -Compress"
        try:
            result = subprocess.run([str(shell), '-NoProfile', '-NonInteractive', '-Command', query],
                                    capture_output=True, timeout=15, creationflags=subprocess.CREATE_NO_WINDOW)
            require(result.returncode == 0 and len(result.stdout) < 4096, 'local boot query failed')
            value = json.loads(result.stdout.decode('utf-8-sig'))
            require(isinstance(value['host'], str) and value['host'] and
                    re.fullmatch(r'\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{7}Z', value['boot']),
                    'local boot identity is invalid')
            return {**value, 'namespace': 'windows-host'}
        except (ValueError, KeyError, subprocess.TimeoutExpired):
            raise LockError('local boot identity is unavailable') from None
    raise LockError('operator locking requires Linux or Windows')


def process_start(pid):
    """Return an OS creation identity, None only for a proven exited process."""
    require(type(pid) is int and 0 < pid <= 0xffffffff, 'invalid owner PID')
    if sys.platform == 'linux':
        try:
            raw = Path('/proc')/str(pid)/'stat'
            fields = raw.read_text(encoding='utf-8').rsplit(')', 1)[1].split()
            require(len(fields) >= 20 and fields[19].isdigit(), 'invalid process stat')
            return None if fields[0] in ('Z', 'X') else fields[19]
        except FileNotFoundError:
            # hidepid/namespace restrictions can hide a live process from /proc.
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                return None
            raise LockError('process exists but its creation identity is unavailable')
        except (IndexError, UnicodeError):
            raise LockError('process identity is unavailable') from None
    if os.name == 'nt':
        from ctypes import wintypes
        kernel = ctypes.WinDLL('kernel32', use_last_error=True)
        kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel.OpenProcess.restype = wintypes.HANDLE
        kernel.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        kernel.WaitForSingleObject.restype = wintypes.DWORD
        kernel.GetProcessTimes.argtypes = [wintypes.HANDLE] + [ctypes.POINTER(wintypes.FILETIME)] * 4
        kernel.GetProcessTimes.restype = wintypes.BOOL
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        kernel.CloseHandle.restype = wintypes.BOOL
        handle = kernel.OpenProcess(0x00100000 | 0x1000, False, pid)  # SYNCHRONIZE | QUERY_LIMITED_INFORMATION
        if not handle:
            if ctypes.get_last_error() == 87: return None  # nonexistent PID
            raise LockError('cannot inspect owner process')
        try:
            state = kernel.WaitForSingleObject(handle, 0)
            if state == 0: return None
            require(state == 258, 'cannot determine owner process liveness')
            stamps = [wintypes.FILETIME() for _ in range(4)]
            require(kernel.GetProcessTimes(handle, *(ctypes.byref(t) for t in stamps)), 'cannot read owner creation time')
            return str((stamps[0].dwHighDateTime << 32) | stamps[0].dwLowDateTime)
        finally:
            kernel.CloseHandle(handle)
    raise LockError('unsupported process identity provider')


def read_owner(path):
    before = checked_stat(path)
    require(0 < before.st_size <= 4096, 'owner record is empty or oversized')
    raw = path.read_bytes()
    after = checked_stat(path)
    require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) ==
            (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), 'owner record changed during inspection')
    try:
        value = json.loads(raw)
        require(isinstance(value, dict) and set(value) == {'schema','pid','process_start','system','token'} and
                value['schema'] == 'xingmang.operator-lock/v1' and type(value['pid']) is int and 0 < value['pid'] <= 0xffffffff and
                isinstance(value['process_start'], str) and value['process_start'].isdigit() and
                isinstance(value['system'], dict) and set(value['system']) == {'host','boot','namespace'} and
                all(isinstance(v, str) and v for v in value['system'].values()) and
                isinstance(value['token'], str) and re.fullmatch(r'[a-f0-9]{32}', value['token']),
                'owner identity is invalid or legacy PID-only; manual recovery required')
    except (ValueError, UnicodeError):
        raise LockError('owner identity is invalid; manual recovery required') from None
    return value, raw


def take_mutex(descriptor):
    os.lseek(descriptor, 0, os.SEEK_SET)
    if os.name == 'nt':
        import msvcrt
        msvcrt.locking(descriptor, msvcrt.LK_NBLCK, 1)
    elif sys.platform == 'linux':
        import fcntl
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
    else:
        raise LockError('unsupported native mutex')


class OwnedLock:
    def __init__(self, path, guard, descriptor, raw):
        self.path, self.guard, self.descriptor, self.raw = path, guard, descriptor, raw

    def release(self):
        if self.descriptor is None: return
        try:
            require(os.path.samestat(os.fstat(self.descriptor), checked_stat(self.guard)), 'mutex inode changed')
            _, raw = read_owner(self.path)
            require(raw == self.raw, 'operator owner changed; record retained')
            self.path.unlink()
        finally:
            os.close(self.descriptor)
            self.descriptor = None


def acquire_lock(state):
    state = checked_path(state)
    require(state.is_dir(), 'operator state directory is absent')
    guard, path = state/'operator.guard', state/'operator.lock'
    checked_path(guard); checked_path(path)
    descriptor = os.open(guard, os.O_CREAT | os.O_RDWR | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    try:
        os.set_inheritable(descriptor, False)
        require(os.path.samestat(os.fstat(descriptor), checked_stat(guard)), 'mutex inode changed')
        take_mutex(descriptor)
        current = system_identity()
        pid = os.getpid()
        started = process_start(pid)
        require(started is not None, 'current process identity is unavailable')
        if path.exists():
            old, raw = read_owner(path)
            require(old['system']['host'] == current['host'], 'owner belongs to a different host')
            if old['system']['boot'] == current['boot']:
                require(old['system']['namespace'] == current['namespace'], 'owner PID namespace cannot be inspected')
                observed = process_start(old['pid'])
                require(observed is None or observed != old['process_start'], 'operator owner is still alive')
            # Different boot or a proven dead/reused PID; preserve the old public evidence.
            require(read_owner(path)[1] == raw, 'owner changed before stale recovery')
            path.rename(state/('operator.lock.stale.'+uuid.uuid4().hex+'.json'))
        value = {'schema':'xingmang.operator-lock/v1', 'pid':pid, 'process_start':started,
                 'system':current, 'token':uuid.uuid4().hex}
        raw = (json.dumps(value, sort_keys=True)+'\n').encode('utf-8')
        # O_EXCL also refuses a concurrent legacy operator that does not take the mutex.
        with path.open('xb') as stream:
            os.chmod(path, 0o600)
            stream.write(raw); stream.flush(); os.fsync(stream.fileno())
        return OwnedLock(path, guard, descriptor, raw)
    except BaseException:
        os.close(descriptor)
        raise
