"""Regression shapes from the reviewed host; no real header values or daemon."""
import copy
import fnmatch
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch
from test_host_nginx import nginx, vhost


class PreservedNginxLayoutTests(unittest.TestCase):
    def test_original_main_glob_can_replace_only_two_frozen_members(self):
        m=nginx(self)
        directory='/www/server/panel/vhost/nginx/'
        main='/www/server/nginx/conf/nginx.conf';stage='/www/server/nginx/conf/unified-stage.conf'
        console=directory+'console.solov.cc.conf';invoice=directory+'invoice.solov.cc.conf'
        other=directory+'0.cloudflare.conf'
        candidate_console='/staged/console.conf';candidate_invoice='/staged/invoice.conf'
        original={'/www/server/nginx/conf/mime.types':'types { text/plain txt; }',
                  main:'events {} http { include mime.types; include '+directory+'*.conf; }',
                  other:'real_ip_header CF-Connecting-IP;',console:vhost(8088),
                  invoice:vhost(58088).replace('console.example.com','invoice.example.com')}
        proposed={key:value for key,value in original.items() if key not in (main,console,invoice)}
        proposed.update({stage:'events {} http { include mime.types; include '+other+'; include '+candidate_console+'; include '+candidate_invoice+'; }',
                         candidate_console:original[console],candidate_invoice:original[invoice].replace(':58088',':58090')})
        # Actual main bytes remain unchanged; only a staged main expands the
        # exact frozen glob at the original AST position.
        before=copy.deepcopy(original)
        m.compare_expanded(original,proposed,main,stage,[(console,candidate_console),(invoice,candidate_invoice)],
                           {directory+'*.conf':[other,console,invoice]})
        self.assertEqual(original,before)


class FrozenHostLayoutTests(unittest.TestCase):
    def setUp(self):
        self.m=nginx(self);self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
        self.sites=self.root/'sites';self.sites.mkdir();self.headers=self.root/'headers';self.headers.mkdir()
        self.candidates=self.root/'candidates';self.candidates.mkdir();self.main=self.root/'nginx.conf';self.stage=self.root/'reviewed-stage.tmp'
        self.mime=self.root/'mime.types';self.mime.write_text('types { text/plain txt; }',encoding='utf-8',newline='\n')
        self.security=self.headers/'security.conf';self.common=self.headers/'common.conf'
        self.security.write_text('add_header X-Frame-Options DENY always;',encoding='utf-8',newline='\n')
        self.common.write_text('proxy_http_version 1.1; proxy_set_header Host $host; proxy_hide_header Server;',encoding='utf-8',newline='\n')
        self.console=self.sites/'console.conf';self.invoice=self.sites/'invoice.conf';self.other=self.sites/'other.conf'
        self.console.write_text(vhost(58080),encoding='utf-8',newline='\n')
        def user(port):
            body='server { listen 443 ssl; server_name invoice.example.com; include '+json.dumps(str(self.security))+'; '
            for modifier,path in [('', '/'),('^~','/api/'),('~','^/api/v1/admin/invoice-requests/[^/]+/documents/upload$'),
                                  ('~','^/api/v1/user/invoice-requests/[^/]+/document$')]:
                body+='location '+modifier+' '+path+' { proxy_pass http://127.0.0.1:'+str(port)+'; include '+json.dumps(str(self.common))+'; } '
            return body+'}'
        self.invoice.write_text(user(58088),encoding='utf-8',newline='\n');self.other.write_text('server { listen 81; server_name other.test; return 204; }',encoding='utf-8')
        self.after_console=self.candidates/'console.conf';self.after_invoice=self.candidates/'invoice.conf'
        self.after_console.write_text(vhost(8088),encoding='utf-8');self.after_invoice.write_text(user(58090),encoding='utf-8')
        self.pattern=str(self.sites/'*.conf')
        self.main.write_text('events {} http { include mime.types; include '+json.dumps(self.pattern)+'; }',encoding='utf-8',newline='\n')
        self.members=[str(self.console),str(self.invoice),str(self.other)]
        self.pairs=[(str(self.console),str(self.after_console)),(str(self.invoice),str(self.after_invoice))]
        self.stage.write_bytes(self.m.staged_main(self.main.read_bytes(),self.pairs,{self.pattern:self.members}))
        smoke=self.root/'smoke.json';smoke.write_text(json.dumps({'origins':{'admin':'https://console.example.com','user':'https://invoice.example.com'}}))
        self.calls=[];self.hook=lambda name,args:None
        def command(name,args):
            self.calls.append((name,args))
            if '-s' in args:return types.SimpleNamespace(stdout=b'',returncode=0)
            primary=Path(args[-1]);prefix=primary.parent;loaded={}
            def visit(path):
                if str(path) in loaded:return
                loaded[str(path)]=path.read_bytes().decode('utf-8')
                def walk(nodes):
                    for head,children in nodes:
                        if head[0]=='include':
                            item=Path(head[1]);item=item if item.is_absolute() else prefix/item
                            matches=sorted(item.parent.iterdir()) if '*' in item.name else [item]
                            for match in matches:
                                if '*' not in item.name or fnmatch.fnmatchcase(match.name,item.name):visit(match)
                        if children is not None:walk(children)
                walk(self.m.parse(loaded[str(path)]))
            visit(primary)
            raw='\n'.join('# configuration file '+path+':\n'+body+'\n' for path,body in loaded.items()).encode()
            self.hook(name,args)
            return types.SimpleNamespace(stdout=raw,returncode=0)
        config={'mode':'server-rehearsal','smoke_config':str(smoke),'candidate':{'projects':[{'kind':'unified','services':{'web':{'role':'web'}}}]},
                'host_nginx':{'main_config':str(self.main),'staged_main_config':str(self.stage),'vhosts':[
                    {'live':str(a),'candidate':str(b),'candidate_sha256':hashlib.sha256(b.read_bytes()).hexdigest()}
                    for a,b in ((self.console,self.after_console),(self.invoice,self.after_invoice))],
                    'web_ports':{'admin':8088,'user':58090},'execution':{'binary':sys.executable,'prefix':str(self.root),'unshare_binary':sys.executable}}}
        ports=[{'target':80,'published':'8088','host_ip':'127.0.0.1'},{'target':8081,'published':'58090','host_ip':'127.0.0.1'}]
        self.driver=types.SimpleNamespace(config=config,output=self.root/'evidence',command=command,
            compose=lambda *a:types.SimpleNamespace(stdout=json.dumps({'services':{'web':{'ports':ports}}}).encode(),returncode=0))

    def test_glob_headers_two_known_regex_and_relative_prefix_roundtrip_without_main_write(self):
        before=self.main.read_bytes();headers={p:p.read_bytes() for p in (self.security,self.common,self.other,self.mime)}
        host=self.m.HostNginx(self.driver);snapshot=host.snapshot()
        self.assertEqual(snapshot['layout']['expanded_globs'],{self.pattern:self.members})
        host.apply(snapshot)
        self.assertEqual(self.invoice.read_bytes(),self.after_invoice.read_bytes())
        for name,args in self.calls:
            if name.endswith('-staged-test'):self.assertEqual(Path(args[-1]).parent,self.main.parent)
        self.after_console.unlink();self.after_invoice.unlink();self.stage.unlink()
        recovering=self.m.HostNginx(self.driver,recovering=True)
        with patch.object(recovering,'probe_restored'):recovering.apply(snapshot,rollback=True)
        self.assertEqual(self.main.read_bytes(),before)
        self.assertTrue(all(p.read_bytes()==body for p,body in headers.items()))
        self.assertFalse(list(self.root.glob('nginx.conf.unified-*.tmp')))

    def test_existing_unknown_regex_or_changed_regex_target_remains_rejected(self):
        body=self.after_invoice.read_text('utf-8');headers={str(p):p.read_text('utf-8') for p in (self.security,self.common)}
        def check(value):self.m.check_routes(value,{'user':'https://invoice.example.com'},{'user':58090},header_files=headers,main_prefix=str(self.root))
        check(body)
        bad_regex_target=body.replace('documents/upload$ { proxy_pass http://127.0.0.1:58090;',
                                      'documents/upload$ { proxy_pass http://127.0.0.1:58088;')
        self.assertNotEqual(bad_regex_target,body)
        for changed in (body.replace('[^/]+','.*',1),body.replace('location ~ ','location ~* ',1),bad_regex_target):
            with self.subTest(changed=changed[:40]),self.assertRaises(self.m.OperatorError):check(changed)
        with self.assertRaises(self.m.OperatorError):self.m.only_proxy_changes(body,body.replace('[^/]+','.*',1))

    def test_header_include_cannot_supply_a_handler_or_recursive_include(self):
        body=self.after_invoice.read_text('utf-8');headers={str(p):p.read_text('utf-8') for p in (self.security,self.common)}
        for fragment in ('return 200;', 'proxy_pass http://127.0.0.1:58088;', 'rewrite ^ /old last;', 'include '+json.dumps(str(self.security))+';',
                         'location / { return 200; }','proxy_set_header Too Many Arguments;'):
            with self.subTest(fragment=fragment),self.assertRaises(self.m.OperatorError):
                self.m.check_routes(body,{'user':'https://invoice.example.com'},{'user':58090},
                                    header_files={**headers,str(self.common):fragment},main_prefix=str(self.root))

    def test_shared_policy_and_even_nonmatching_directory_members_cannot_change_after_snapshot(self):
        host=self.m.HostNginx(self.driver);snapshot=host.snapshot();original=self.invoice.read_bytes()
        for path in (self.security,self.common,self.other,self.mime):
            saved=path.read_bytes();path.write_bytes(saved+b'\n# concurrent policy edit')
            with self.subTest(path=path.name),patch.object(self.m,'atomic_public_file') as install,self.assertRaises(self.m.OperatorError):host.apply(snapshot)
            install.assert_not_called();self.assertEqual(self.invoice.read_bytes(),original);path.write_bytes(saved)
        added=self.sites/'not-loaded.tmp';added.write_bytes(b'not included')
        with patch.object(self.m,'atomic_public_file') as install,self.assertRaises(self.m.OperatorError):host.apply(snapshot)
        install.assert_not_called();added.unlink()

    def test_glob_membership_change_during_staged_test_is_rejected_before_live_install(self):
        host=self.m.HostNginx(self.driver);snapshot=host.snapshot();before=self.invoice.read_bytes()
        def hook(name,args):
            if name=='host-nginx-switch-staged-test':(self.sites/'late.conf').write_text('server { listen 99; }')
        self.hook=hook
        with patch.object(self.m,'atomic_public_file') as install,self.assertRaises(self.m.OperatorError):host.apply(snapshot)
        install.assert_not_called();self.assertEqual(self.invoice.read_bytes(),before)

    def test_shared_header_change_after_live_test_prevents_reload(self):
        host=self.m.HostNginx(self.driver);snapshot=host.snapshot();self.calls.clear()
        def hook(name,args):
            if name=='host-nginx-switched-test':self.common.write_text('proxy_set_header Host changed;')
        self.hook=hook
        with self.assertRaises(self.m.OperatorError):host.apply(snapshot)
        self.assertFalse(any('-s' in args for _,args in self.calls))

    def test_snapshot_cannot_rebaseline_a_shared_change_after_first_preflight(self):
        host=self.m.HostNginx(self.driver);host.preflight()
        self.security.write_bytes(self.security.read_bytes()+b'\n# external edit')
        with self.assertRaises(self.m.OperatorError):host.snapshot()

    def test_live_test_cannot_revert_to_the_other_known_vhost_before_reload(self):
        host=self.m.HostNginx(self.driver);snapshot=host.snapshot();original=self.invoice.read_bytes()
        candidate=self.after_invoice.read_bytes()
        def switch_hook(name,args):
            if name=='host-nginx-switched-test':self.invoice.write_bytes(original)
        self.hook=switch_hook;self.calls.clear()
        with self.assertRaises(self.m.OperatorError):host.apply(snapshot)
        self.assertFalse(any('-s' in args for _,args in self.calls))
        self.hook=lambda *args:None;host.apply(snapshot)
        def restore_hook(name,args):
            if name=='host-nginx-restored-test':self.invoice.write_bytes(candidate)
        self.hook=restore_hook;self.calls.clear()
        with patch.object(host,'probe_restored'),self.assertRaises(self.m.OperatorError):host.apply(snapshot,rollback=True)
        self.assertFalse(any('-s' in args for _,args in self.calls))

    def test_second_preflight_cannot_rebaseline_policy_changed_after_its_initial_check(self):
        host=self.m.HostNginx(self.driver);host.preflight();compose=self.driver.compose
        def changed(*args):
            self.common.write_text('proxy_http_version 1.1; proxy_set_header Host new-public-host;')
            return compose(*args)
        self.driver.compose=changed
        with self.assertRaises(self.m.OperatorError):host.snapshot()

    def test_duplicate_glob_member_references_and_changed_include_positions_are_rejected(self):
        self.main.write_text('events {} http { include mime.types; include '+json.dumps(str(self.console))+'; include '+json.dumps(self.pattern)+'; }')
        self.stage.write_bytes(self.m.staged_main(self.main.read_bytes(),self.pairs,{self.pattern:self.members}))
        with self.assertRaisesRegex(self.m.OperatorError,'exactly once'):self.m.HostNginx(self.driver).preflight()


if __name__=='__main__':unittest.main()
