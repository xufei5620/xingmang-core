"""Original resolved-mount consumer: one documented legacy injection, no secret reads."""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT/'deploy/rehearsal-unified'))
import lifecycle


class SecretSourceTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.public=self.root/'original-compose.json'; self.public.write_text('{}')
        self.env=self.root/'original.env'; self.env.write_text('PUBLIC_FIXTURE=true\n')
        self.project={'name':'old-platform','kind':'platform','compose_files':[str(self.public)],'env_file':str(self.env),
                      'services':{'postgres':{'role':'platform-postgres'}}}
        self.resolved={'services':{'postgres':{'environment':{'POSTGRES_PASSWORD_FILE':'/run/secrets/database__postgres_password'},
                      'secrets':[{'source':'database__postgres_password','target':'database__postgres_password'}]}},
                      'secrets':{'database__postgres_password':{'environment':'DATABASE_PASSWORD'}}}
        self.driver=object.__new__(lifecycle.DockerDriver)
        self.driver.projects=lambda _: [self.project]
        self.driver.compose=lambda *args:subprocess.CompletedProcess([],0,json.dumps(self.resolved).encode(),b'')

    def test_shipped_candidate_pg_secret_is_file_backed_and_consumed_as_readonly_mount(self):
        compose=json.loads((ROOT/'deploy/unified/compose.json').read_text())
        definition=compose['secrets']['database__postgres_password']
        self.assertEqual(definition,{'file':'${SECRETS_DIR:?set SECRETS_DIR}/database__postgres_password'})
        self.resolved['secrets']['database__postgres_password']={'file':str(self.root/'password-not-opened')}
        with patch.object(Path,'open',side_effect=AssertionError('secret content must not be opened')):
            rows=self.driver.resolved_mount_inventory()
        self.assertEqual(rows[0]['mounts'],[{'type':'bind','source':str(self.root/'password-not-opened'),
                          'target':'/run/secrets/database__postgres_password','rw':False}])

    def test_exact_original_pg_injection_is_classified_without_a_fictitious_bind(self):
        try: rows=self.driver.resolved_mount_inventory('previous')
        except lifecycle.OperatorError: self.fail('known original PG injection still blocks old-plan verification')
        self.assertEqual(rows[0]['mounts'],[])
        proof=self.driver.legacy_secret_inputs
        self.assertEqual(len(proof),1)
        self.assertEqual(proof[0]['type'],'compose-environment-injected')
        self.assertEqual(proof[0]['env_sha256'],lifecycle.digest(self.env))
        self.assertEqual(proof[0]['compose'][0]['sha256'],lifecycle.digest(self.public))
        self.assertNotIn('content',proof[0])

    def test_legacy_exception_is_not_available_to_other_sides_roles_sources_or_targets(self):
        with self.assertRaises(lifecycle.OperatorError): self.driver.resolved_mount_inventory('candidate')
        original=copy.deepcopy(self.resolved); project=copy.deepcopy(self.project)
        for field,value in [('kind','invoice'),('role','invoice-postgres'),('environment','OTHER_PASSWORD'),
                            ('target','other-password'),('content','not-a-secret'),('file','/fake'),('uid','1000')]:
            with self.subTest(field=field):
                self.resolved=copy.deepcopy(original); self.project=copy.deepcopy(project)
                if field=='kind': self.project['kind']=value
                elif field=='role': self.project['services']['postgres']['role']=value
                elif field in ('target','uid'): self.resolved['services']['postgres']['secrets'][0][field]=value
                else: self.resolved['secrets']['database__postgres_password'][field]=value
                with self.assertRaises(lifecycle.OperatorError): self.driver.resolved_mount_inventory('previous')

    def test_injected_target_cannot_be_shadowed_by_a_mount_or_parent_mount(self):
        for target in ('/run/secrets/database__postgres_password','/run/secrets','/run'):
            self.resolved['services']['postgres']['volumes']=[{'type':'bind','source':'/other','target':target,'read_only':True}]
            with self.subTest(target=target), self.assertRaises(lifecycle.OperatorError):
                self.driver.resolved_mount_inventory('previous')

    def test_regular_file_secret_rejects_conflicting_or_inline_sources(self):
        for secondary in ('environment','content','external'):
            self.resolved['secrets']['database__postgres_password']={'file':'/safe','environment':'DATABASE_PASSWORD'}
            self.resolved['secrets']['database__postgres_password']={'file':'/safe',secondary:True if secondary=='external' else 'x'}
            with self.subTest(secondary=secondary), self.assertRaises(lifecycle.OperatorError):
                self.driver.resolved_mount_inventory()

    def test_legacy_provenance_is_rechecked_before_old_inputs_are_consumed(self):
        self.driver.resolved_mount_inventory('previous')
        old=copy.deepcopy(self.driver.legacy_secret_inputs)
        self.env.write_text('PUBLIC_FIXTURE=changed\n')
        snapshot={'project_files':[{'env_file':str(self.env),'env_sha256':lifecycle.digest(self.env),'compose':[]}],
                  'legacy_secret_inputs':old}
        with self.assertRaises(lifecycle.OperatorError): lifecycle.require_snapshot_files(snapshot)


if __name__=='__main__':unittest.main()
