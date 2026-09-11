"""Real local junction/symlink boundaries; no Docker or build is executed."""
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

spec=importlib.util.spec_from_file_location('unified_path_gate',Path(__file__).resolve().parents[1]/'unified-service.py')
gate=importlib.util.module_from_spec(spec);spec.loader.exec_module(gate)


class ReachedSafeBoundary(AssertionError):
    pass


def redirect(link,target):
    if os.name=='nt':
        # A junction is a real reparse point and does not require developer mode.
        quote=lambda value:"'"+str(value).replace("'","''")+"'"
        script='New-Item -ItemType Junction -Path '+quote(link)+' -Target '+quote(target)+' -ErrorAction Stop | Out-Null'
        result=subprocess.run(['pwsh','-NoProfile','-NonInteractive','-Command',script],capture_output=True,text=True)
        if result.returncode:raise RuntimeError('Cannot create required real junction: '+result.stderr)
        if not link.is_junction():raise AssertionError('junction fixture is not a real reparse point')
    else:
        link.symlink_to(target,target_is_directory=target.is_dir())
        if not link.is_symlink():raise AssertionError('symlink fixture was not created')


def inventory(root):
    return sorted((str(path.relative_to(root)),path.read_bytes() if path.is_file() else None)
                  for path in root.rglob('*') if not path.is_symlink() and not path.is_junction())


class UnifiedPathTests(unittest.TestCase):
    def setUp(self):
        self.temporary=tempfile.TemporaryDirectory(prefix='xm-unified-path-')
        self.addCleanup(self.temporary.cleanup)
        self.base=Path(self.temporary.name)
        self.source=self.base/'source';self.source.mkdir()
        self.outside=self.base/'outside';self.outside.mkdir()
        (self.outside/'kept.txt').write_text('untouched synthetic evidence',encoding='utf-8')

    def build(self,path,accepted):
        before=inventory(self.outside)
        with mock.patch.object(gate,'check'),mock.patch.object(gate,'source_provenance',return_value={}),mock.patch.object(gate,'definitions',return_value=[]),mock.patch.object(gate,'docker_command',side_effect=ReachedSafeBoundary) as docker:
            with self.assertRaises(ReachedSafeBoundary if accepted else gate.GateError):
                gate.build(self.source,'synthetic',str(path),'test-local')
            self.assertEqual(docker.call_count,1 if accepted else 0)
        self.assertEqual(inventory(self.outside),before)

    def test_build_normal_new_and_missing_parent_are_accepted(self):
        self.build(self.base/'new-output',True)
        self.build(self.base/'missing'/'deeper'/'new-output',True)

    def test_build_nonempty_existing_and_file_leaf_are_rejected(self):
        self.build(self.outside,False)
        self.build(self.outside/'kept.txt',False)
        empty=self.base/'empty';empty.mkdir();self.build(empty,False)

    def test_build_leaf_parent_and_ancestor_redirect_are_rejected_before_docker(self):
        alias=self.base/'alias';redirect(alias,self.outside)
        for path in (alias,alias/'new-output',alias/'missing'/'deeper'/'new-output'):
            with self.subTest(path=path):self.build(path,False)

    def test_build_redirect_before_dotdot_cannot_be_normalized_away(self):
        alias=self.base/'alias';redirect(alias,self.outside)
        self.build(alias/'..'/'new-output',False)

    def verify(self,path,accepted):
        before=inventory(self.outside)
        with mock.patch.object(Path,'read_text',side_effect=ReachedSafeBoundary) as parsed:
            with self.assertRaises(ReachedSafeBoundary if accepted else gate.GateError):
                gate.verify(self.source,str(path),'test-local')
            self.assertEqual(parsed.call_count,1 if accepted else 0)
        self.assertEqual(inventory(self.outside),before)

    def test_verify_regular_manifest_is_accepted(self):
        path=self.outside/'manifest.json';path.write_text('{}',encoding='utf-8')
        self.verify(path,True)

    def test_verify_parent_and_ancestor_redirect_rejected_before_manifest_parse(self):
        folder=self.outside/'deeper';folder.mkdir();(folder/'manifest.json').write_text('{}',encoding='utf-8')
        alias=self.base/'alias';redirect(alias,self.outside)
        self.verify(alias/'deeper'/'manifest.json',False)

    def test_verify_manifest_leaf_reparse_is_rejected_before_read(self):
        # Directory junctions are tested as leaf paths on Windows; symlink files
        # are additionally exercised on POSIX where no privilege is required.
        path=self.outside/'manifest.json';path.write_text('{}',encoding='utf-8')
        alias=self.base/'manifest-link'
        redirect(alias,self.outside if os.name=='nt' else path)
        self.verify(alias,False)

    def test_verify_missing_manifest_fails_closed(self):
        with self.assertRaises((gate.GateError,OSError)):
            gate.verify(self.source,str(self.base/'missing'/'manifest.json'),'test-local')

    def test_artifact_normal_and_existing_internal_redirect(self):
        images=self.outside/'images';images.mkdir();(images/'asset.tar').write_bytes(b'fixture')
        self.assertEqual(gate.artifact_path(self.outside,'images/asset.tar'),images/'asset.tar')
        alias=self.outside/'alias';redirect(alias,images)
        with self.assertRaises(gate.GateError):gate.artifact_path(self.outside,'alias/asset.tar')
        with self.assertRaises(gate.GateError):gate.artifact_path(self.outside,'alias')

    def test_artifact_root_ancestor_redirect_rejected(self):
        (self.outside/'asset.tar').write_bytes(b'fixture')
        alias=self.base/'alias';redirect(alias,self.outside)
        with self.assertRaises(gate.GateError):gate.artifact_path(alias,'asset.tar')

    def test_artifact_parent_traversal_and_absolute_paths_stay_rejected(self):
        for path in ('../outside/kept.txt',str(self.outside/'kept.txt')):
            with self.subTest(path=path),self.assertRaises(gate.GateError):
                gate.artifact_path(self.outside,path)


if __name__=='__main__':unittest.main(verbosity=2)
